package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Runs only when TEST_DATABASE_URL points at a throwaway database; it
// truncates the tables. Example:
//
//	TEST_DATABASE_URL=postgres://absensi:absensi@localhost:5432/absensi_test?sslmode=disable go test ./internal/db/
func testStore(t *testing.T) *Store {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE users, audit_log RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGuardsAndBinding(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const admin, worker, other = int64(100), int64(200), int64(300)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.EnsureAdminByID(ctx, admin))
	must(s.AddUser(ctx, admin, "08123456789", "Budi"))
	if err := s.AddUser(ctx, admin, "+62 812-3456-789", "Dup"); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate phone: %v", err)
	}

	// Binding
	if _, err := s.BindTelegram(ctx, "6280000000", worker); !errors.Is(err, ErrPhoneUnknown) {
		t.Errorf("unknown phone: %v", err)
	}
	u, err := s.BindTelegram(ctx, "628123456789", worker)
	if err != nil || u.TelegramID != worker || u.Name != "Budi" {
		t.Fatalf("bind: %+v %v", u, err)
	}
	if _, err := s.BindTelegram(ctx, "628123456789", worker); err != nil {
		t.Errorf("re-bind same account should be ok: %v", err)
	}
	if _, err := s.BindTelegram(ctx, "628123456789", other); !errors.Is(err, ErrBoundElsewhere) {
		t.Errorf("bound elsewhere: %v", err)
	}
	must(s.AddUser(ctx, admin, "0813000000", "Sari"))
	if _, err := s.BindTelegram(ctx, "62813000000", worker); !errors.Is(err, ErrAccountInUse) {
		t.Errorf("account in use: %v", err)
	}

	// Admin guards
	if err := s.SetActive(ctx, admin, "100", false); !errors.Is(err, ErrSelfAction) {
		t.Errorf("self revoke: %v", err)
	}
	if err := s.SetRole(ctx, admin, "100", RoleWorker); !errors.Is(err, ErrSelfAction) {
		t.Errorf("self demote: %v", err)
	}
	must(s.SetRole(ctx, admin, "08123456789", RoleAdmin))
	// worker (now admin) tries to remove the original admin: allowed, 2 admins exist
	must(s.SetRole(ctx, worker, "100", RoleWorker))
	// now worker is the last admin; another admin-less actor can't remove them
	if err := s.SetActive(ctx, other, "08123456789", false); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("last admin: %v", err)
	}

	// Revoke / rebind
	must(s.SetActive(ctx, worker, "0813000000", false))
	if _, err := s.BindTelegram(ctx, "62813000000", other); !errors.Is(err, ErrInactive) {
		t.Errorf("inactive: %v", err)
	}
	must(s.Unbind(ctx, admin, "08123456789"))
	if got, _ := s.GetByPhone(ctx, "628123456789"); got.TelegramID != 0 {
		t.Errorf("unbind left telegram id %d", got.TelegramID)
	}
	must(s.SetPhone(ctx, admin, "08123456789", "0819999999"))
	if _, err := s.GetByPhone(ctx, "62819999999"); err != nil {
		t.Errorf("setphone: %v", err)
	}
}

func TestImportUser(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const admin = int64(100)
	if err := s.EnsureAdminByID(ctx, admin); err != nil {
		t.Fatal(err)
	}

	check := func(phone, name, role, want string) {
		t.Helper()
		got, err := s.ImportUser(ctx, admin, phone, name, role)
		if err != nil || got != want {
			t.Errorf("import %s %s %s: got %q, %v; want %q", phone, name, role, got, err, want)
		}
	}
	check("628111111111", "Budi", "", ImportAdded)
	check("628222222222", "Sari", RoleAdmin, ImportAdded)
	check("628111111111", "Budi", "", ImportSame)
	check("628111111111", "Budi", RoleWorker, ImportSame)
	check("628111111111", "Budi Santoso", "", ImportUpdated)
	check("628222222222", "Sari", RoleWorker, ImportUpdated)

	b, _ := s.GetByPhone(ctx, "628111111111")
	sr, _ := s.GetByPhone(ctx, "628222222222")
	if b.Name != "Budi Santoso" || b.Role != RoleWorker || sr.Role != RoleWorker || !sr.IsActive {
		t.Errorf("state: %+v %+v", b, sr)
	}
	es, _ := s.RecentAudit(ctx, 50)
	if len(es) < 5 {
		t.Errorf("expected audit entries, got %d", len(es))
	}
}

func TestReminderGroups(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE reminder_groups`); err != nil {
		t.Fatal(err)
	}
	const chat = int64(-100123)
	if err := s.SetReminderTime(ctx, 1, chat, "Kantor", RemindIn, "12:00"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReminderTime(ctx, 1, chat, "Kantor", RemindOut, "23:00"); err != nil {
		t.Fatal(err)
	}
	g, err := s.GetReminderGroup(ctx, chat)
	if err != nil || g.InTime != "12:00" || g.OutTime != "23:00" || g.Title != "Kantor" {
		t.Fatalf("group = %+v, %v", g, err)
	}

	day := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if ok, err := s.MarkReminderSent(ctx, chat, RemindIn, day); err != nil || !ok {
		t.Fatalf("first mark: %v %v", ok, err)
	}
	if ok, _ := s.MarkReminderSent(ctx, chat, RemindIn, day); ok {
		t.Error("second mark on the same day must return false")
	}
	if ok, _ := s.MarkReminderSent(ctx, chat, RemindOut, day); !ok {
		t.Error("check-out reminder is tracked separately")
	}
	if ok, _ := s.MarkReminderSent(ctx, chat, RemindIn, day.AddDate(0, 0, 1)); !ok {
		t.Error("next day must be allowed")
	}

	if err := s.MigrateReminderGroup(ctx, chat, -100999); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReminderTime(ctx, 1, -100999, "Kantor", RemindIn, ""); err != nil {
		t.Fatal(err)
	}
	gs, _ := s.ListReminderGroups(ctx)
	if len(gs) != 1 || gs[0].ChatID != -100999 || gs[0].InTime != "" || gs[0].OutTime != "23:00" {
		t.Errorf("groups = %+v", gs)
	}
	if err := s.SetReminderTime(ctx, 1, -100999, "Kantor", RemindOut, ""); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.ListReminderGroups(ctx); len(gs) != 0 {
		t.Errorf("fully disabled group should not be listed: %+v", gs)
	}

	for in, want := range map[string]string{"9:00": "09:00", "09.30": "09:30", "23:00": "23:00"} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("ParseClock(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"25:00", "jam 9", ""} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) should fail", bad)
		}
	}
}

func TestHolidays(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE holidays`); err != nil {
		t.Fatal(err)
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	defaults := []Holiday{{Day: day(12, 24), Name: "Cuti bersama Natal", Kind: "cuti_bersama"}, {Day: day(12, 25), Name: "Natal", Kind: "nasional"}}
	if err := s.SeedHolidays(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	// HR: the 24th is a working day; the 31st is an office holiday.
	if name, err := s.CancelHoliday(ctx, 1, day(12, 24)); err != nil || name != "Cuti bersama Natal" {
		t.Fatalf("cancel: %q %v", name, err)
	}
	if _, err := s.CancelHoliday(ctx, 1, day(12, 24)); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel twice: %v", err)
	}
	if err := s.SetHoliday(ctx, 1, day(12, 31), "Tutup akhir tahun"); err != nil {
		t.Fatal(err)
	}
	// A restart re-seeds the defaults: the cancelled day must stay cancelled.
	if err := s.SeedHolidays(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	hs, err := s.HolidaysBetween(ctx, day(12, 1), day(12, 1).AddDate(0, 1, 0))
	if err != nil || len(hs) != 3 {
		t.Fatalf("holidays = %+v, %v", hs, err)
	}
	got := map[string]Holiday{}
	for _, h := range hs {
		got[h.Key()] = h
	}
	if got["2026-12-24"].Active || !got["2026-12-25"].Active || !got["2026-12-31"].Active || got["2026-12-31"].Kind != "kantor" {
		t.Errorf("state = %+v", got)
	}
	// Re-adding a cancelled day re-activates it with HR's name.
	if err := s.SetHoliday(ctx, 1, day(12, 24), "Libur Natal kantor"); err != nil {
		t.Fatal(err)
	}
	hs, _ = s.HolidaysBetween(ctx, day(12, 24), day(12, 25))
	if len(hs) != 1 || !hs[0].Active || hs[0].Name != "Libur Natal kantor" {
		t.Errorf("re-added = %+v", hs)
	}
}
