package db

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const (
	RoleAdmin  = "admin"
	RoleWorker = "worker"
)

var (
	ErrNotFound    = errors.New("user tidak ditemukan")
	ErrExists      = errors.New("nomor/ID sudah terdaftar")
	ErrSelfAction  = errors.New("tidak bisa melakukan ini pada akun sendiri")
	ErrLastAdmin   = errors.New("tidak bisa menghapus/menurunkan admin aktif terakhir")
	ErrInvalidRole = errors.New("role harus admin atau worker")
	ErrBadPhone    = errors.New("nomor telepon tidak valid (contoh: 08123456789 atau +628123456789)")

	ErrPhoneUnknown   = errors.New("nomor ini belum didaftarkan HR")
	ErrInactive       = errors.New("akses untuk nomor ini sudah dicabut")
	ErrBoundElsewhere = errors.New("nomor ini sudah terhubung ke akun Telegram lain, minta HR menjalankan /unbind")
	ErrAccountInUse   = errors.New("akun Telegram ini sudah terhubung ke nomor lain")
)

type User struct {
	ID         int64
	Phone      string // normalised digits, "" if unknown
	TelegramID int64  // 0 until verified
	Name       string
	Role       string
	IsActive   bool
	CreatedAt  time.Time
}

func (u User) Verified() bool { return u.TelegramID != 0 }

type AuditEntry struct {
	Actor  string
	Action string
	Target string
	Detail string
	At     time.Time
}

// NormalizePhone turns "0812-3456-789", "+62 812 3456 789" or "62812…" into
// digits with the Indonesian country code: 62812…. It rejects anything that
// is not 9–15 digits (E.164 maximum is 15).
func NormalizePhone(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", ErrBadPhone
		}
	}
	d := b.String()
	switch {
	case strings.HasPrefix(d, "0"):
		d = "62" + d[1:]
	case strings.HasPrefix(d, "8"): // 812… typed without the leading 0
		d = "62" + d
	}
	if len(d) < 9 || len(d) > 15 {
		return "", ErrBadPhone
	}
	return d, nil
}

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies every embedded migration in filename order. Migrations are
// written to be idempotent (IF NOT EXISTS).
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := migrationFS.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("migration %s: %w", n, err)
		}
	}
	return nil
}

const userCols = `id, COALESCE(phone,''), COALESCE(telegram_id,0), name, role, is_active, created_at`

func scanUser(r pgx.Row) (*User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Phone, &u.TelegramID, &u.Name, &u.Role, &u.IsActive, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) GetByTelegramID(ctx context.Context, tgID int64) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE telegram_id=$1`, tgID))
}

func (s *Store) GetByPhone(ctx context.Context, phone string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE phone=$1`, phone))
}

// Resolve finds a user from what an admin typed: a phone number in any
// common format, or (for bootstrap admins without a phone) a Telegram ID.
func (s *Store) Resolve(ctx context.Context, ref string) (*User, error) {
	if p, err := NormalizePhone(ref); err == nil {
		if u, err := s.GetByPhone(ctx, p); err == nil {
			return u, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64); err == nil && id > 0 {
		return s.GetByTelegramID(ctx, id)
	}
	return nil, ErrNotFound
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY is_active DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Phone, &u.TelegramID, &u.Name, &u.Role, &u.IsActive, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// EnsureAdminByID upserts a bootstrap admin known by Telegram ID.
func (s *Store) EnsureAdminByID(ctx context.Context, tgID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (telegram_id, name, role, is_active)
		VALUES ($1, 'HR Admin', 'admin', TRUE)
		ON CONFLICT (telegram_id) DO UPDATE SET role='admin', is_active=TRUE, updated_at=now()`, tgID)
	return err
}

// EnsureAdminByPhone upserts a bootstrap admin known by phone; they become
// usable once they share their contact with the bot.
func (s *Store) EnsureAdminByPhone(ctx context.Context, phone string) error {
	p, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO users (phone, name, role, is_active)
		VALUES ($1, 'HR Admin', 'admin', TRUE)
		ON CONFLICT (phone) DO UPDATE SET role='admin', is_active=TRUE, updated_at=now()`, p)
	return err
}

// AddUser pre-registers a worker by phone. The Telegram account is bound later.
func (s *Store) AddUser(ctx context.Context, actor int64, phone, name string) error {
	p, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	ct, err := s.pool.Exec(ctx, `
		INSERT INTO users (phone, name, role, created_by) VALUES ($1,$2,'worker',$3)
		ON CONFLICT (phone) DO NOTHING`, p, name, actor)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrExists
	}
	return s.Audit(ctx, actor, "add_user", p, map[string]string{"name": name})
}

// BindTelegram links a verified Telegram account to a pre-registered phone.
// Only succeeds for an active, still-unbound row (or the same account again).
func (s *Store) BindTelegram(ctx context.Context, phone string, tgID int64) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		UPDATE users SET telegram_id=$2, updated_at=now()
		WHERE phone=$1 AND is_active AND telegram_id IS NULL
		RETURNING `+userCols, phone, tgID))
	if err == nil {
		_ = s.Audit(ctx, tgID, "bind", phone, nil)
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		if strings.Contains(err.Error(), "users_telegram_id_key") {
			return nil, ErrAccountInUse
		}
		return nil, err
	}
	// Work out why the UPDATE matched nothing.
	cur, gerr := s.GetByPhone(ctx, phone)
	switch {
	case errors.Is(gerr, ErrNotFound):
		return nil, ErrPhoneUnknown
	case gerr != nil:
		return nil, gerr
	case !cur.IsActive:
		return nil, ErrInactive
	case cur.TelegramID == tgID:
		return cur, nil
	default:
		return nil, ErrBoundElsewhere
	}
}

// SetActive grants (true) or revokes (false) access. Guards: no self-revoke,
// never leave the system without an active admin.
func (s *Store) SetActive(ctx context.Context, actor int64, ref string, active bool) error {
	action := "revoke"
	if active {
		action = "grant"
	}
	return s.mutate(ctx, actor, ref, action, nil, !active, func(u *User) error {
		if !active && u.TelegramID == actor {
			return ErrSelfAction
		}
		return nil
	}, `UPDATE users SET is_active=$2, updated_at=now() WHERE id=$1`, active)
}

func (s *Store) SetRole(ctx context.Context, actor int64, ref, role string) error {
	if role != RoleAdmin && role != RoleWorker {
		return ErrInvalidRole
	}
	return s.mutate(ctx, actor, ref, "set_role", map[string]string{"role": role}, role == RoleWorker, func(u *User) error {
		if role == RoleWorker && u.TelegramID == actor {
			return ErrSelfAction
		}
		return nil
	}, `UPDATE users SET role=$2, updated_at=now() WHERE id=$1`, role)
}

func (s *Store) SetName(ctx context.Context, actor int64, ref, name string) error {
	return s.mutate(ctx, actor, ref, "set_name", map[string]string{"name": name}, false, nil,
		`UPDATE users SET name=$2, updated_at=now() WHERE id=$1`, name)
}

// SetPhone replaces the phone number and clears the Telegram binding, so the
// worker must verify the new number by sharing their contact again.
func (s *Store) SetPhone(ctx context.Context, actor int64, ref, newPhone string) error {
	p, err := NormalizePhone(newPhone)
	if err != nil {
		return err
	}
	err = s.mutate(ctx, actor, ref, "set_phone", map[string]string{"phone": p}, false, func(u *User) error {
		if u.TelegramID == actor {
			return ErrSelfAction
		}
		return nil
	}, `UPDATE users SET phone=$2, telegram_id=NULL, updated_at=now() WHERE id=$1`, p)
	if err != nil && strings.Contains(err.Error(), "users_phone_key") {
		return ErrExists
	}
	return err
}

// Unbind clears the Telegram binding (e.g. the worker got a new Telegram account).
func (s *Store) Unbind(ctx context.Context, actor int64, ref string) error {
	return s.mutate(ctx, actor, ref, "unbind", nil, false, func(u *User) error {
		if u.TelegramID == actor {
			return ErrSelfAction
		}
		if u.Phone == "" {
			return errors.New("user ini tidak punya nomor telepon; set dulu dengan /setphone")
		}
		return nil
	}, `UPDATE users SET telegram_id=NULL, updated_at=now() WHERE id=$1`)
}

// mutate resolves ref, locks the row, runs the pre-check, optionally enforces
// the last-admin rule, executes the update (arg is $2) and audits it.
func (s *Store) mutate(ctx context.Context, actor int64, ref, action string, detail any, removesAdmin bool,
	check func(*User) error, query string, args ...any) error {

	target, err := s.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1 FOR UPDATE`, target.ID)
	u, err := scanUser(row)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(u); err != nil {
			return err
		}
	}
	if removesAdmin && u.Role == RoleAdmin && u.IsActive {
		// Lock the set of active admins so concurrent removals serialise.
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM users WHERE role='admin' AND is_active FOR UPDATE) a`).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx, query, append([]any{u.ID}, args...)...); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	tgt := u.Phone
	if tgt == "" {
		tgt = strconv.FormatInt(u.TelegramID, 10)
	}
	return s.Audit(ctx, actor, action, tgt, detail)
}

// Audit records an event. actor is a Telegram ID (0 = system).
func (s *Store) Audit(ctx context.Context, actor int64, action, target string, detail any) error {
	b, err := json.Marshal(detail)
	if err != nil || detail == nil {
		b = []byte("{}")
	}
	who := "system"
	if actor != 0 {
		who = strconv.FormatInt(actor, 10)
	}
	var tgt *string
	if target != "" {
		tgt = &target
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail) VALUES ($1,$2,$3,$4)`,
		who, action, tgt, b)
	return err
}

func (s *Store) RecentAudit(ctx context.Context, n int) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT actor, action, COALESCE(target,''), detail::text, at
		FROM audit_log ORDER BY at DESC, id DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.Actor, &e.Action, &e.Target, &e.Detail, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Import outcomes.
const (
	ImportAdded   = "added"
	ImportUpdated = "updated"
	ImportSame    = "same"
)

// ImportUser adds a user by phone or, if the phone exists, updates name and
// (when role != "") role. All changes go through the normal guarded methods,
// so self-demotion and last-admin rules still apply and every change is audited.
func (s *Store) ImportUser(ctx context.Context, actor int64, phone, name, role string) (string, error) {
	cur, err := s.GetByPhone(ctx, phone)
	if errors.Is(err, ErrNotFound) {
		if err := s.AddUser(ctx, actor, phone, name); err != nil {
			return "", err
		}
		if role == RoleAdmin {
			if err := s.SetRole(ctx, actor, phone, RoleAdmin); err != nil {
				return "", err
			}
		}
		return ImportAdded, nil
	}
	if err != nil {
		return "", err
	}
	result := ImportSame
	if name != cur.Name {
		if err := s.SetName(ctx, actor, phone, name); err != nil {
			return "", err
		}
		result = ImportUpdated
	}
	if role != "" && role != cur.Role {
		if err := s.SetRole(ctx, actor, phone, role); err != nil {
			return "", err
		}
		result = ImportUpdated
	}
	return result, nil
}

// DeleteByPhonePrefix permanently deletes users whose phone starts with
// prefix. Only used by the demo seeder to remove its own fake users.
func (s *Store) DeleteByPhonePrefix(ctx context.Context, prefix string) (int64, error) {
	if len(prefix) < 8 {
		return 0, errors.New("prefix too short; refusing a broad delete")
	}
	ct, err := s.pool.Exec(ctx, `DELETE FROM users WHERE phone LIKE $1 || '%'`, prefix)
	return ct.RowsAffected(), err
}

// BackdateCreatedAt sets created_at for users whose phone starts with prefix.
// Only used by the demo seeder, so demo people look registered before their
// generated history begins.
func (s *Store) BackdateCreatedAt(ctx context.Context, prefix string, t time.Time) error {
	if len(prefix) < 8 {
		return errors.New("prefix too short")
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET created_at=$2 WHERE phone LIKE $1 || '%'`, prefix, t)
	return err
}

// ---- reminder groups ----

const (
	RemindIn  = "in"  // check-in reminder
	RemindOut = "out" // check-out reminder
)

type ReminderGroup struct {
	ChatID      int64
	Title       string
	InTime      string // "HH:MM" or "" (off)
	OutTime     string
	LastInSent  time.Time // zero if never
	LastOutSent time.Time
}

// ParseClock validates "9:00", "09.00" or "17:30" and returns "HH:MM".
func ParseClock(s string) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ".", ":")
	t, err := time.Parse("15:04", s)
	if err != nil {
		return "", errors.New("format jam harus HH:MM, mis. 09:00 atau 17:30")
	}
	return t.Format("15:04"), nil
}

func (s *Store) GetReminderGroup(ctx context.Context, chatID int64) (*ReminderGroup, error) {
	var g ReminderGroup
	var lastIn, lastOut *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT chat_id, title, COALESCE(in_time,''), COALESCE(out_time,''), last_in_sent, last_out_sent
		FROM reminder_groups WHERE chat_id=$1`, chatID).Scan(&g.ChatID, &g.Title, &g.InTime, &g.OutTime, &lastIn, &lastOut)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if lastIn != nil {
		g.LastInSent = *lastIn
	}
	if lastOut != nil {
		g.LastOutSent = *lastOut
	}
	return &g, err
}

func (s *Store) ListReminderGroups(ctx context.Context) ([]ReminderGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT chat_id, title, COALESCE(in_time,''), COALESCE(out_time,''), last_in_sent, last_out_sent
		FROM reminder_groups WHERE in_time IS NOT NULL OR out_time IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReminderGroup
	for rows.Next() {
		var g ReminderGroup
		var lastIn, lastOut *time.Time
		if err := rows.Scan(&g.ChatID, &g.Title, &g.InTime, &g.OutTime, &lastIn, &lastOut); err != nil {
			return nil, err
		}
		if lastIn != nil {
			g.LastInSent = *lastIn
		}
		if lastOut != nil {
			g.LastOutSent = *lastOut
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetReminderTime sets (clock "HH:MM") or clears (clock "") one reminder kind.
func (s *Store) SetReminderTime(ctx context.Context, actor, chatID int64, title, kind, clock string) error {
	col := "in_time"
	if kind == RemindOut {
		col = "out_time"
	}
	var v *string
	if clock != "" {
		v = &clock
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reminder_groups (chat_id, title, `+col+`, updated_by) VALUES ($1,$2,$3,$4)
		ON CONFLICT (chat_id) DO UPDATE SET title=EXCLUDED.title, `+col+`=EXCLUDED.`+col+`,
			updated_by=EXCLUDED.updated_by, updated_at=now()`, chatID, title, v, actor)
	if err != nil {
		return err
	}
	return s.Audit(ctx, actor, "reminder_"+kind, strconv.FormatInt(chatID, 10), map[string]string{"group": title, "time": clock})
}

// MarkReminderSent records that today's reminder of kind went out. It
// returns false if another run already marked it (so it is sent only once).
func (s *Store) MarkReminderSent(ctx context.Context, chatID int64, kind string, day time.Time) (bool, error) {
	col := "last_in_sent"
	if kind == RemindOut {
		col = "last_out_sent"
	}
	ct, err := s.pool.Exec(ctx, `UPDATE reminder_groups SET `+col+`=$2::date
		WHERE chat_id=$1 AND (`+col+` IS NULL OR `+col+` < $2::date)`, chatID, day.Format("2006-01-02"))
	return ct.RowsAffected() == 1, err
}

// MigrateReminderGroup follows a group that Telegram upgraded to a supergroup.
func (s *Store) MigrateReminderGroup(ctx context.Context, from, to int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE reminder_groups SET chat_id=$2 WHERE chat_id=$1`, from, to)
	return err
}

// DeleteReminderGroup stops reminders for a group (e.g. the bot was removed).
func (s *Store) DeleteReminderGroup(ctx context.Context, chatID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM reminder_groups WHERE chat_id=$1`, chatID)
	return err
}

// ---- holidays ----

type Holiday struct {
	Day    time.Time // date (UTC midnight as returned by Postgres)
	Name   string
	Kind   string // holiday.Kind*
	Active bool   // false = cancelled: a normal working day
	Source string // "skb" or "hr"
}

// Key is the date as "YYYY-MM-DD".
func (h Holiday) Key() string { return h.Day.Format("2006-01-02") }

// SeedHolidays inserts official defaults without touching days HR already
// changed or cancelled.
func (s *Store) SeedHolidays(ctx context.Context, days []Holiday) error {
	for _, h := range days {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO holidays (day, name, kind, source) VALUES ($1::date, $2, $3, 'skb')
			ON CONFLICT (day) DO NOTHING`, h.Key(), h.Name, h.Kind); err != nil {
			return err
		}
	}
	return nil
}

// HolidaysBetween returns holidays (active and cancelled) with day in [from, to).
func (s *Store) HolidaysBetween(ctx context.Context, from, to time.Time) ([]Holiday, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day, name, kind, active, source FROM holidays
		WHERE day >= $1::date AND day < $2::date ORDER BY day`, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Holiday
	for rows.Next() {
		var h Holiday
		if err := rows.Scan(&h.Day, &h.Name, &h.Kind, &h.Active, &h.Source); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetHoliday makes day a holiday named name. A new day is an office
// holiday; an existing (e.g. cancelled SKB) day is re-activated and renamed.
func (s *Store) SetHoliday(ctx context.Context, actor int64, day time.Time, name string) error {
	key := day.Format("2006-01-02")
	_, err := s.pool.Exec(ctx, `
		INSERT INTO holidays (day, name, kind, source, updated_by) VALUES ($1::date, $2, 'kantor', 'hr', $3)
		ON CONFLICT (day) DO UPDATE SET name=EXCLUDED.name, active=TRUE, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		key, name, actor)
	if err != nil {
		return err
	}
	return s.Audit(ctx, actor, "holiday_set", key, map[string]string{"name": name})
}

// CancelHoliday turns a holiday into a normal working day. Returns
// ErrNotFound if day is not an active holiday.
func (s *Store) CancelHoliday(ctx context.Context, actor int64, day time.Time) (string, error) {
	key := day.Format("2006-01-02")
	var name string
	err := s.pool.QueryRow(ctx, `
		UPDATE holidays SET active=FALSE, updated_by=$2, updated_at=now()
		WHERE day=$1::date AND active RETURNING name`, key, actor).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return name, s.Audit(ctx, actor, "holiday_cancel", key, map[string]string{"name": name})
}
