package bot

import (
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/db"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var workerMenu = []tele.Command{
	{Text: "datang", Description: "Catat datang (atau chat biasa: \"saya sudah datang\")"},
	{Text: "pulang", Description: "Catat pulang (opsional: 5 Januari 17.00)"},
	{Text: "status", Description: "Status kehadiran hari ini"},
	{Text: "riwayat", Description: "Riwayat kehadiran terakhir"},
	{Text: "libur", Description: "Daftar hari libur"},
	{Text: "help", Description: "Bantuan"},
}

var adminMenu = append(append([]tele.Command{}, workerMenu...),
	tele.Command{Text: "today", Description: "Kehadiran semua karyawan hari ini"},
	tele.Command{Text: "summary", Description: "Rekap mingguan/bulanan (file Excel)"},
	tele.Command{Text: "users", Description: "Daftar pengguna"},
	tele.Command{Text: "adduser", Description: "Tambah pengguna"},
	tele.Command{Text: "revoke", Description: "Cabut akses pengguna"},
	tele.Command{Text: "grant", Description: "Aktifkan kembali pengguna"},
	tele.Command{Text: "setrole", Description: "Ubah role pengguna"},
	tele.Command{Text: "setphone", Description: "Ganti nomor telepon"},
	tele.Command{Text: "setname", Description: "Ubah nama"},
	tele.Command{Text: "unbind", Description: "Lepas akun Telegram pengguna"},
	tele.Command{Text: "import", Description: "Import pengguna dari file xlsx/csv"},
	tele.Command{Text: "audit", Description: "Log perubahan akses"},
)

// groupMenu is shown in groups; every command there just points to the private chat.
var groupMenu = []tele.Command{
	{Text: "datang", Description: "Absen datang (lewat chat pribadi)"},
	{Text: "pulang", Description: "Absen pulang (lewat chat pribadi)"},
	{Text: "help", Description: "Cara memakai bot absensi"},
}

func menuFor(role string) []tele.Command {
	if role == db.RoleAdmin {
		return adminMenu
	}
	return workerMenu
}

const workerHelp = `Absensi — cara pakai:

Tulis saja seperti chat biasa, mis.:
  "saya sudah datang di kantor"
  "baru sampai, WFH hari ini"
  "pulang jam 5 sore"
Bot akan menanyakan WFO/WFH (jika belum disebut) dan meminta lokasimu.
Lokasi wajib untuk datang, boleh dilewati untuk pulang.

Atau pakai perintah:
/datang [waktu] [wfo|wfh]   /pulang [waktu]
  contoh: /datang 5 Januari 08.00 wfo
          /pulang 5 Januari 5.00PM  (tanpa AM/PM = 24 jam)
/batal — batalkan permintaan yang sedang berjalan
/status — catatan hari ini
/riwayat — 10 catatan terakhir
/libur — daftar hari libur
/help — bantuan`

const adminHelp = `

Perintah admin (HR):
/today — siapa yang sudah/belum hadir hari ini
/summary [week|month|YYYY-MM] [nomor] — rekap kehadiran (dikirim sebagai file Excel)
  atau tulis saja: "rekap bulan september", "rekap fajar minggu lalu", "laporan 1-15 september"
/users — daftar pengguna
/adduser <nomor telepon> <nama> — daftarkan pekerja (mereka verifikasi sendiri lewat /start)
/import — daftarkan banyak pekerja sekaligus dari file .xlsx/.csv
/libur tambah <tanggal> <keterangan> · /libur hapus <tanggal> — atur hari libur (lihat /libur help)

Di grup kantor (tambahkan bot ke grup, ketik /help di sana):
/ingatkan [pulang] — tag anggota yang belum absen
/jadwal on — pengingat otomatis Senin–Jumat (datang 12:00, pulang 23:00)
/revoke <nomor> — cabut akses
/grant <nomor> — aktifkan kembali
/setrole <nomor> admin|worker — ubah role
/setphone <nomor lama> <nomor baru> — ganti nomor (perlu verifikasi ulang)
/unbind <nomor> — lepas akun Telegram (mis. ganti akun)
/setname <nomor> <nama> — ubah nama
/audit [n] — log perubahan terakhir`

func helpText(role string) string {
	if role == db.RoleAdmin {
		return strings.TrimSpace(workerHelp + adminHelp)
	}
	return workerHelp
}
