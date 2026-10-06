package bot

import (
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/db"
	"github.com/absensi/internal/importer"
)

// Bulk user import: an admin uploads an .xlsx/.csv, gets a preview, and the
// changes are applied only after they tap "Terapkan".

const (
	maxImportBytes = 1 << 20
	importTTL      = 10 * time.Minute
)

type importPlan struct {
	file    string
	rows    []importer.Row
	expires time.Time
}

var (
	importMarkup = &tele.ReplyMarkup{}
	btnImportYes = importMarkup.Data("✅ Terapkan", "imp", "yes")
	btnImportNo  = importMarkup.Data("❌ Batal", "imp", "no")
)

func init() {
	importMarkup.Inline(importMarkup.Row(btnImportYes, btnImportNo))
}

const importHelp = `Import pengguna dari file:
Kirim file .xlsx atau .csv ke chat ini (sebagai dokumen). Baris pertama = header:

  telepon | nama | role (opsional)

• telepon: 08…, +62… atau 62… (kolom boleh bernama telepon, no hp, phone, wa)
• nama: nama lengkap
• role: admin atau worker; kosong = worker untuk orang baru, tidak diubah untuk yang sudah ada

Nomor yang sudah terdaftar akan diperbarui nama/role-nya. Tidak ada yang dicabut aksesnya.
Bot menampilkan pratinjau dulu; perubahan baru diterapkan setelah kamu menekan "Terapkan".
Maksimum 500 baris, 1 MB.`

func (h *Handler) cmdImport(c tele.Context) error { return c.Send(importHelp) }

func (h *Handler) onDocument(c tele.Context) error {
	doc := c.Message().Document
	if doc == nil {
		return nil
	}
	name := doc.FileName
	if l := strings.ToLower(name); !strings.HasSuffix(l, ".xlsx") && !strings.HasSuffix(l, ".csv") {
		return c.Send("Hanya file .xlsx atau .csv yang bisa diimport. Ketik /import untuk format.")
	}
	if doc.FileSize > maxImportBytes {
		return c.Send("File terlalu besar (maksimum 1 MB).")
	}

	rc, err := h.bot.File(&doc.File)
	if err != nil {
		log.Printf("download %s: %v", name, err)
		return c.Send("Gagal mengunduh file, coba kirim lagi.")
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxImportBytes+1))
	rc.Close()
	if err != nil || len(data) > maxImportBytes {
		return c.Send("Gagal membaca file (atau lebih dari 1 MB).")
	}

	rows, rowErrs, err := importer.Parse(name, data)
	if err != nil {
		return c.Send("⚠️ " + err.Error() + "\n\nKetik /import untuk format.")
	}

	ctx, cancel := ctxTimeout()
	defer cancel()
	var added, updated, same []string
	for _, r := range rows {
		cur, err := h.users.GetByPhone(ctx, r.Phone)
		switch {
		case errors.Is(err, db.ErrNotFound):
			added = append(added, fmt.Sprintf("%s (%s)%s", r.Name, r.Phone, roleNote(r.Role, "")))
		case err != nil:
			return adminErr(c, "import preview", err)
		case cur.Name != r.Name || (r.Role != "" && r.Role != cur.Role):
			updated = append(updated, describeUpdate(cur, r))
		default:
			same = append(same, r.Phone)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📥 Pratinjau import %s\n\n➕ Baru: %d\n✏️ Diperbarui: %d\n= Tidak berubah: %d\n⚠️ Dilewati (error): %d\n",
		name, len(added), len(updated), len(same), len(rowErrs))
	listSection(&sb, "Baru", added)
	listSection(&sb, "Diperbarui", updated)
	if len(rowErrs) > 0 {
		sb.WriteString("\nError:\n")
		for i, e := range rowErrs {
			if i == 15 {
				fmt.Fprintf(&sb, "…dan %d lainnya\n", len(rowErrs)-15)
				break
			}
			fmt.Fprintf(&sb, "• baris %d: %s\n", e.Line, e.Msg)
		}
	}

	if len(added)+len(updated) == 0 {
		sb.WriteString("\nTidak ada perubahan untuk diterapkan.")
		return sendLong(c, sb.String())
	}
	h.mu.Lock()
	h.imports[user(c).TelegramID] = &importPlan{file: name, rows: rows, expires: time.Now().Add(importTTL)}
	h.mu.Unlock()

	sb.WriteString("\nTerapkan perubahan ini?")
	text := sb.String()
	if len(text) > 3800 { // keep the buttons on a single message
		text = text[:3800] + "\n…(dipotong)\n\nTerapkan perubahan ini?"
	}
	return c.Send(text, importMarkup)
}

func roleNote(newRole, oldRole string) string {
	if newRole == db.RoleAdmin && oldRole != db.RoleAdmin {
		return " [admin]"
	}
	return ""
}

func describeUpdate(cur *db.User, r importer.Row) string {
	var parts []string
	if cur.Name != r.Name {
		parts = append(parts, fmt.Sprintf("nama %q → %q", cur.Name, r.Name))
	}
	if r.Role != "" && r.Role != cur.Role {
		parts = append(parts, fmt.Sprintf("role %s → %s", cur.Role, r.Role))
	}
	s := fmt.Sprintf("%s: %s", r.Phone, strings.Join(parts, ", "))
	if !cur.IsActive {
		s += " (nonaktif, tetap nonaktif)"
	}
	return s
}

func listSection(sb *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n%s:\n", title)
	for i, it := range items {
		if i == 20 {
			fmt.Fprintf(sb, "…dan %d lainnya\n", len(items)-20)
			return
		}
		fmt.Fprintf(sb, "• %s\n", it)
	}
}

func (h *Handler) onImportConfirm(c tele.Context) error {
	admin := user(c).TelegramID
	h.mu.Lock()
	plan := h.imports[admin]
	delete(h.imports, admin) // one shot: a second tap can't re-apply
	h.mu.Unlock()

	_ = c.Respond()
	if c.Callback().Data != "yes" {
		return c.Edit("Import dibatalkan.")
	}
	if plan == nil || time.Now().After(plan.expires) {
		return c.Edit("Pratinjau sudah kedaluwarsa. Kirim ulang filenya.")
	}
	_ = c.Edit(fmt.Sprintf("⏳ Menerapkan import %s…", plan.file))

	ctx, cancel := ctxTimeout()
	defer cancel()
	counts := map[string]int{}
	var failures []string
	for _, r := range plan.rows {
		res, err := h.users.ImportUser(ctx, admin, r.Phone, r.Name, r.Role)
		if err != nil {
			failures = append(failures, fmt.Sprintf("baris %d (%s): %v", r.Line, r.Phone, err))
			continue
		}
		counts[res]++
		if res != db.ImportSame {
			h.refreshMenu(r.Phone)
		}
	}
	_ = h.users.Audit(ctx, admin, "import", plan.file, map[string]int{
		"added": counts[db.ImportAdded], "updated": counts[db.ImportUpdated], "failed": len(failures),
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ Import %s selesai\n➕ Ditambahkan: %d\n✏️ Diperbarui: %d\n= Tidak berubah: %d\n",
		plan.file, counts[db.ImportAdded], counts[db.ImportUpdated], counts[db.ImportSame])
	if len(failures) > 0 {
		fmt.Fprintf(&sb, "⚠️ Gagal: %d\n", len(failures))
		for _, f := range failures {
			sb.WriteString("• " + f + "\n")
		}
	}
	if counts[db.ImportAdded] > 0 {
		sb.WriteString("\nPengguna baru perlu membuka bot, kirim /start, lalu membagikan nomor teleponnya untuk verifikasi.")
	}
	return sendLong(c, sb.String())
}
