// Package importer reads a user list (phone, name, optional role) from an
// .xlsx or .csv file uploaded by an admin.
package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/absensi/internal/db"
)

const MaxRows = 500

var ErrNoHeader = errors.New("baris pertama harus berisi header: kolom telepon dan nama (role opsional)")

// Row is one valid line of the file.
type Row struct {
	Line  int    // 1-based line in the file, for error messages
	Phone string // normalised
	Name  string
	Role  string // "", db.RoleAdmin or db.RoleWorker ("" = not specified)
}

type RowError struct {
	Line int
	Msg  string
}

var (
	phoneHeaders = set("telepon", "telpon", "nomor", "nomor telepon", "no telepon", "no hp", "nohp", "no_hp", "hp", "phone", "whatsapp", "wa", "no wa", "nomor hp")
	nameHeaders  = set("nama", "name", "nama lengkap")
	roleHeaders  = set("role", "peran", "akses")
)

func set(vs ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// Parse reads data according to the extension of filename (.xlsx or .csv).
func Parse(filename string, data []byte) ([]Row, []RowError, error) {
	var recs [][]string
	var err error
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx":
		recs, err = readXLSX(data)
	case ".csv", ".txt":
		recs, err = readCSV(data)
	default:
		return nil, nil, errors.New("format file tidak didukung, kirim .xlsx atau .csv")
	}
	if err != nil {
		return nil, nil, err
	}
	return parseRecords(recs)
}

func readXLSX(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("file xlsx tidak bisa dibaca: %w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("file xlsx kosong")
	}
	return f.GetRows(sheets[0])
}

func readCSV(data []byte) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // UTF-8 BOM from Excel
	// Indonesian Excel exports with ';', others with ','.
	first := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		first = data[:i]
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = ','
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		r.Comma = ';'
	}
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("file csv tidak bisa dibaca: %w", err)
	}
	return recs, nil
}

func parseRecords(recs [][]string) ([]Row, []RowError, error) {
	if len(recs) == 0 {
		return nil, nil, ErrNoHeader
	}
	pc, nc, rc := -1, -1, -1
	for i, h := range recs[0] {
		h = strings.ToLower(strings.TrimSpace(h))
		switch {
		case phoneHeaders[h]:
			pc = i
		case nameHeaders[h]:
			nc = i
		case roleHeaders[h]:
			rc = i
		}
	}
	if pc < 0 || nc < 0 {
		return nil, nil, ErrNoHeader
	}
	if len(recs)-1 > MaxRows {
		return nil, nil, fmt.Errorf("terlalu banyak baris (maksimum %d)", MaxRows)
	}

	var rows []Row
	var errs []RowError
	seen := map[string]int{}
	for i, rec := range recs[1:] {
		line := i + 2
		cell := func(c int) string {
			if c < 0 || c >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[c])
		}
		rawPhone, name, rawRole := fixNumber(cell(pc)), cell(nc), strings.ToLower(cell(rc))
		if rawPhone == "" && name == "" && rawRole == "" {
			continue // blank line
		}
		phone, err := db.NormalizePhone(rawPhone)
		switch {
		case err != nil:
			errs = append(errs, RowError{line, fmt.Sprintf("nomor tidak valid: %q", rawPhone)})
			continue
		case name == "":
			errs = append(errs, RowError{line, "nama kosong"})
			continue
		}
		role := ""
		switch rawRole {
		case "":
		case db.RoleAdmin, db.RoleWorker:
			role = rawRole
		default:
			errs = append(errs, RowError{line, fmt.Sprintf("role harus admin atau worker, bukan %q", rawRole)})
			continue
		}
		if first, dup := seen[phone]; dup {
			errs = append(errs, RowError{line, fmt.Sprintf("nomor %s sama dengan baris %d", phone, first)})
			continue
		}
		seen[phone] = line
		rows = append(rows, Row{Line: line, Phone: phone, Name: name, Role: role})
	}
	return rows, errs, nil
}

// fixNumber undoes spreadsheet damage to phone numbers stored as numbers,
// e.g. "6.28123E+11" -> "628123000000".
func fixNumber(s string) string {
	if strings.ContainsAny(s, "eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return strconv.FormatFloat(f, 'f', 0, 64)
		}
	}
	return s
}
