// Command demoseed fills the database and spreadsheet with fictional
// employees and attendance so the bot can be demoed to HR.
//
//	go run ./cmd/demoseed           # remove old demo data, then seed fresh
//	go run ./cmd/demoseed -clean    # only remove demo data
//	go run ./cmd/demoseed -all      # also wipe real users' attendance rows, then seed
//
// Demo users have phones 0800-0000-01xx (toll-free range, never a real
// mobile) and negative Telegram IDs (never a real account), so nobody can
// log in as them and cleanup can find them exactly. Stop the bot while this
// runs: the cleanup rewrites month tabs.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/config"
	"github.com/absensi/internal/db"
	"github.com/absensi/internal/geocode"
	"github.com/absensi/internal/holiday"
	"github.com/absensi/internal/sheets"
)

const phonePrefix = "62800000" // 0800-000-… ; DeleteByPhonePrefix target

type point struct{ lat, lng float64 }

type profile struct {
	name, phone string
	tgID        int64
	role        string
	home        point
	wfh         float64 // chance of WFH on a normal day
	late        float64 // chance of arriving late
	absent      float64 // chance of no attendance (leave/sick)
	forgetOut   float64 // chance of forgetting to check out
	verified    bool
	revokedFrom time.Time // zero = still active
}

func main() {
	clean := flag.Bool("clean", false, "only remove demo data")
	office := flag.String("office", "-6.224100,106.808400", "office coordinates lat,lng for WFO check-ins")
	from := flag.String("from", "2026-09-01", "first day of generated attendance")
	all := flag.Bool("all", false, "also delete NON-demo attendance rows in the period (fresh start for a demo)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	store, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
	sh, err := sheets.New(ctx, cfg.CredentialsFile, cfg.SpreadsheetID, cfg.Location)
	if err != nil {
		log.Fatal(err)
	}
	loc := cfg.Location
	start, err := time.ParseInLocation("2006-01-02", *from, loc)
	if err != nil {
		log.Fatalf("-from: %v", err)
	}
	now := time.Now().In(loc)

	// 1. Remove previous demo data.
	n, err := store.DeleteByPhonePrefix(ctx, phonePrefix)
	if err != nil {
		log.Fatal(err)
	}
	rows, err := sh.DeleteWhere(ctx, start.AddDate(0, -1, 0), now.AddDate(0, 0, 1),
		func(r attendance.Record) bool { return *all || r.TelegramID < 0 })
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("removed %d demo users and %d demo sheet rows", n, rows)
	if *clean {
		return
	}

	off, err := parsePoint(*office)
	if err != nil {
		log.Fatalf("-office: %v", err)
	}
	people := demoPeople(start)

	// 2. Users, through the normal store methods (audited as "system").
	for _, p := range people {
		must(store.AddUser(ctx, 0, p.phone, p.name))
		if p.role == db.RoleAdmin {
			must(store.SetRole(ctx, 0, p.phone, db.RoleAdmin))
		}
		if p.verified {
			if _, err := store.BindTelegram(ctx, p.phone, p.tgID); err != nil {
				log.Fatalf("bind %s: %v", p.name, err)
			}
		}
		if !p.revokedFrom.IsZero() {
			must(store.SetActive(ctx, 0, p.phone, false))
		}
	}
	must(store.BackdateCreatedAt(ctx, phonePrefix, start.AddDate(0, 0, -1)))
	log.Printf("added %d demo users", len(people))

	// 3. Addresses: one Nominatim lookup per distinct place (their policy: ≤1 req/s).
	geo := geocode.New(cfg.GeocodeAgent)
	addr := map[point]string{}
	places := []point{off}
	for _, p := range people {
		places = append(places, p.home)
	}
	for _, pt := range places {
		if _, ok := addr[pt]; ok {
			continue
		}
		a, err := geo.Reverse(ctx, pt.lat, pt.lng)
		if err != nil {
			log.Printf("geocode %v: %v (address left empty)", pt, err)
		}
		addr[pt] = a
		time.Sleep(1100 * time.Millisecond)
	}

	// 4. Attendance.
	isHoliday := map[string]bool{} // official holidays: nobody comes in
	for _, d := range holiday.Defaults {
		isHoliday[d.Date] = true
	}
	rng := rand.New(rand.NewPCG(2026, 10))
	var recs []attendance.Record
	for day := start; !day.After(now); day = day.AddDate(0, 0, 1) {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday || isHoliday[day.Format("2006-01-02")] {
			continue
		}
		for _, p := range people {
			if !p.verified || (!p.revokedFrom.IsZero() && !day.Before(p.revokedFrom)) {
				continue
			}
			if rng.Float64() < p.absent {
				continue
			}
			recs = append(recs, genDay(rng, p, day, now, off, addr)...)
		}
	}
	if err := sh.AppendMany(ctx, recs); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %d attendance rows (%s … %s)", len(recs), start.Format("2 Jan"), now.Format("2 Jan 15:04"))
}

func genDay(rng *rand.Rand, p profile, day, now time.Time, office point, addr map[point]string) []attendance.Record {
	at := func(h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, rng.IntN(60), 0, day.Location())
	}
	status := attendance.StatusWFO
	wfh := p.wfh
	if day.Weekday() == time.Friday {
		wfh += 0.2 // WFH Fridays are popular
	}
	if rng.Float64() < wfh {
		status = attendance.StatusWFH
	}
	place := office
	if status == attendance.StatusWFH {
		place = p.home
	}
	jitter := func(pt point) (float64, float64) { // a few metres of GPS noise
		return pt.lat + (rng.Float64()-0.5)*0.0004, pt.lng + (rng.Float64()-0.5)*0.0004
	}
	base := attendance.Record{TelegramID: p.tgID, Name: p.name, Phone: p.phone, Status: status, Source: attendance.SourceLive}

	// Check-in: usually 07:30–08:15, late arrivals 08:30–09:40.
	inMin := 7*60 + 30 + rng.IntN(45)
	if rng.Float64() < p.late {
		inMin = 8*60 + 30 + rng.IntN(70)
	}
	in := base
	in.Type, in.At = attendance.TypeIn, at(inMin/60, inMin%60)
	in.RecordedAt = in.At
	in.HasLocation = true
	in.Lat, in.Lng = jitter(place)
	in.Address = addr[place]
	if rng.Float64() < 0.06 { // forgot, entered the time manually a bit later
		in.Source = attendance.SourceManual
		in.RecordedAt = in.At.Add(time.Duration(60+rng.IntN(120)) * time.Minute)
	}
	if in.At.After(now) || in.RecordedAt.After(now) {
		return nil
	}
	out := []attendance.Record{in}

	if rng.Float64() < p.forgetOut {
		return out // incomplete day: shows a warning in /summary
	}
	outMin := 17*60 + rng.IntN(90)
	if day.Weekday() == time.Friday {
		outMin -= 30
	}
	o := base
	o.Type, o.At = attendance.TypeOut, at(outMin/60, outMin%60)
	o.RecordedAt = o.At
	if rng.Float64() < 0.7 { // location is optional on check-out
		o.HasLocation = true
		o.Lat, o.Lng = jitter(place)
		o.Address = addr[place]
	}
	if rng.Float64() < 0.05 { // remembered the next morning
		o.Source = attendance.SourceManual
		o.RecordedAt = time.Date(day.Year(), day.Month(), day.Day()+1, 7, 50+rng.IntN(9), rng.IntN(60), 0, day.Location())
	}
	if o.At.After(now) || o.RecordedAt.After(now) {
		return out // still at work
	}
	return append(out, o)
}

func demoPeople(start time.Time) []profile {
	// Homes around Greater Jakarta.
	depok := point{-6.402500, 106.794200}
	bekasi := point{-6.238300, 106.975600}
	tangsel := point{-6.288600, 106.717900}
	bogor := point{-6.595000, 106.816600}
	jaktim := point{-6.225000, 106.900400}
	jaksel := point{-6.261500, 106.810600}
	tangerang := point{-6.178300, 106.631900}
	jakbar := point{-6.168300, 106.758900}

	ph := func(i int) string { return fmt.Sprintf("%s01%02d", phonePrefix, i) }
	return []profile{
		{name: "Andi Pratama", phone: ph(1), tgID: -9001, role: db.RoleWorker, home: depok, wfh: 0.10, late: 0.05, absent: 0.03, forgetOut: 0.02, verified: true},
		{name: "Budi Santoso", phone: ph(2), tgID: -9002, role: db.RoleWorker, home: bekasi, wfh: 0.05, late: 0.35, absent: 0.05, forgetOut: 0.03, verified: true},
		{name: "Citra Lestari", phone: ph(3), tgID: -9003, role: db.RoleWorker, home: tangsel, wfh: 0.25, late: 0.08, absent: 0.04, forgetOut: 0.02, verified: true},
		{name: "Dewi Anggraini (HR)", phone: ph(4), tgID: -9004, role: db.RoleAdmin, home: jaksel, wfh: 0.05, late: 0.02, absent: 0.02, forgetOut: 0.01, verified: true},
		{name: "Eko Saputra", phone: ph(5), tgID: -9005, role: db.RoleWorker, home: bogor, wfh: 0.60, late: 0.10, absent: 0.04, forgetOut: 0.05, verified: true},
		{name: "Fajar Nugroho", phone: ph(6), tgID: -9006, role: db.RoleWorker, home: jaktim, wfh: 0.15, late: 0.15, absent: 0.05, forgetOut: 0.12, verified: true},
		{name: "Gita Permata", phone: ph(7), tgID: -9007, role: db.RoleWorker, home: tangerang, wfh: 0.20, late: 0.05, absent: 0.03, forgetOut: 0.02, verified: true},
		{name: "Hendra Wijaya", phone: ph(8), tgID: -9008, role: db.RoleWorker, home: jakbar, wfh: 0.10, late: 0.20, absent: 0.15, forgetOut: 0.04, verified: true},
		// Registered by HR but never opened the bot: shows ⏳ in /users and "belum hadir" in /today.
		{name: "Indah Sari", phone: ph(9), tgID: -9009, role: db.RoleWorker, home: depok, verified: false},
		// Left the company mid-September: shows ⛔ in /users, still appears in the September summary.
		{name: "Joko Susilo", phone: ph(10), tgID: -9010, role: db.RoleWorker, home: bekasi, wfh: 0.10, late: 0.10, absent: 0.05, forgetOut: 0.03, verified: true,
			revokedFrom: start.AddDate(0, 0, 14)},
	}
}

func parsePoint(s string) (point, error) {
	a, b, ok := strings.Cut(s, ",")
	if !ok {
		return point{}, fmt.Errorf("want lat,lng")
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil {
		return point{}, fmt.Errorf("want lat,lng")
	}
	return point{lat, lng}, nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
