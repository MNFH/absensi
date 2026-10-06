// Package holiday holds Indonesia's official national holidays (libur
// nasional) and joint leave days (cuti bersama), loaded into the database as
// defaults that HR can cancel or extend.
package holiday

const (
	KindNational = "nasional"     // libur nasional
	KindJoint    = "cuti_bersama" // cuti bersama
	KindOffice   = "kantor"       // added by HR
)

type Default struct {
	Date string // YYYY-MM-DD
	Name string
	Kind string
}

// Defaults come from the SKB 3 Menteri (Menteri Agama, Menteri
// Ketenagakerjaan, Menteri PANRB):
//   - 2026: No. 1497/2/5 Tahun 2025, 17 libur nasional + 8 cuti bersama
//     https://www.kemenkopmk.go.id/pemerintah-tetapkan-17-hari-libur-nasional-dan-8-hari-cuti-bersama-tahun-2026
//   - 2027: signed 15 Sep 2026, 18 libur nasional + 8 cuti bersama
//     https://www.kemenkopmk.go.id/pemerintah-tetapkan-18-hari-libur-nasional-dan-8-cuti-bersama-tahun-2027
//
// Cuti bersama formally applies to civil servants; private companies may
// work on those days, which HR can reflect with /libur hapus.
var Defaults = []Default{
	// 2026 — libur nasional
	{"2026-01-01", "Tahun Baru 2026 Masehi", KindNational},
	{"2026-01-16", "Isra Mikraj Nabi Muhammad SAW", KindNational},
	{"2026-02-17", "Tahun Baru Imlek 2577 Kongzili", KindNational},
	{"2026-03-19", "Hari Suci Nyepi (Tahun Baru Saka 1948)", KindNational},
	{"2026-03-21", "Idul Fitri 1447 H", KindNational},
	{"2026-03-22", "Idul Fitri 1447 H", KindNational},
	{"2026-04-03", "Wafat Yesus Kristus", KindNational},
	{"2026-04-05", "Kebangkitan Yesus Kristus (Paskah)", KindNational},
	{"2026-05-01", "Hari Buruh Internasional", KindNational},
	{"2026-05-14", "Kenaikan Yesus Kristus", KindNational},
	{"2026-05-27", "Idul Adha 1447 H", KindNational},
	{"2026-05-31", "Hari Raya Waisak 2570 BE", KindNational},
	{"2026-06-01", "Hari Lahir Pancasila", KindNational},
	{"2026-06-16", "1 Muharam Tahun Baru Islam 1448 H", KindNational},
	{"2026-08-17", "Proklamasi Kemerdekaan RI", KindNational},
	{"2026-08-25", "Maulid Nabi Muhammad SAW", KindNational},
	{"2026-12-25", "Kelahiran Yesus Kristus (Natal)", KindNational},
	// 2026 — cuti bersama
	{"2026-02-16", "Cuti bersama Tahun Baru Imlek", KindJoint},
	{"2026-03-18", "Cuti bersama Hari Suci Nyepi", KindJoint},
	{"2026-03-20", "Cuti bersama Idul Fitri 1447 H", KindJoint},
	{"2026-03-23", "Cuti bersama Idul Fitri 1447 H", KindJoint},
	{"2026-03-24", "Cuti bersama Idul Fitri 1447 H", KindJoint},
	{"2026-05-15", "Cuti bersama Kenaikan Yesus Kristus", KindJoint},
	{"2026-05-28", "Cuti bersama Idul Adha 1447 H", KindJoint},
	{"2026-12-24", "Cuti bersama Natal", KindJoint},

	// 2027 — libur nasional
	{"2027-01-01", "Tahun Baru 2027 Masehi", KindNational},
	{"2027-01-05", "Isra Mikraj Nabi Muhammad SAW 1448 H", KindNational},
	{"2027-02-06", "Tahun Baru Imlek 2578 Kongzili", KindNational},
	{"2027-03-08", "Hari Suci Nyepi (Tahun Baru Saka 1949)", KindNational},
	{"2027-03-10", "Idul Fitri 1448 H", KindNational},
	{"2027-03-11", "Idul Fitri 1448 H", KindNational},
	{"2027-03-26", "Wafat Yesus Kristus", KindNational},
	{"2027-03-28", "Kebangkitan Yesus Kristus (Paskah)", KindNational},
	{"2027-05-01", "Hari Buruh Internasional", KindNational},
	{"2027-05-06", "Kenaikan Yesus Kristus", KindNational},
	{"2027-05-17", "Idul Adha 1448 H", KindNational},
	{"2027-05-20", "Hari Raya Waisak 2571 BE", KindNational},
	{"2027-06-01", "Hari Lahir Pancasila", KindNational},
	{"2027-06-06", "1 Muharam Tahun Baru Islam 1449 H", KindNational},
	{"2027-08-15", "Maulid Nabi Muhammad SAW", KindNational},
	{"2027-08-17", "Proklamasi Kemerdekaan RI", KindNational},
	{"2027-12-25", "Kelahiran Yesus Kristus (Natal)", KindNational},
	{"2027-12-26", "Isra Mikraj Nabi Muhammad SAW 1449 H", KindNational},
	// 2027 — cuti bersama
	{"2027-02-05", "Cuti bersama Tahun Baru Imlek", KindJoint},
	{"2027-03-09", "Cuti bersama Idul Fitri 1448 H", KindJoint},
	{"2027-03-12", "Cuti bersama Idul Fitri 1448 H", KindJoint},
	{"2027-03-15", "Cuti bersama Idul Fitri 1448 H", KindJoint},
	{"2027-03-25", "Cuti bersama Wafat Yesus Kristus", KindJoint},
	{"2027-05-18", "Cuti bersama Idul Adha 1448 H", KindJoint},
	{"2027-05-19", "Cuti bersama Hari Raya Waisak", KindJoint},
	{"2027-12-24", "Cuti bersama Natal", KindJoint},
}

// KindLabel is the Indonesian label shown to users.
func KindLabel(kind string) string {
	switch kind {
	case KindNational:
		return "Libur nasional"
	case KindJoint:
		return "Cuti bersama"
	}
	return "Libur kantor"
}
