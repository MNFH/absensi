package importer

import (
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestParseCSV(t *testing.T) {
	csv := "\xef\xbb\xbfNama;Telepon;Role\n" +
		"Budi;0812-3456-789;\n" +
		"Sari;+62 811 222 333;ADMIN\n" +
		";;\n" + // blank
		"Dodi;abc;worker\n" + // bad phone
		"Eka;08123456789;worker\n" + // duplicate of Budi
		"Fani;0813000111;boss\n" + // bad role
		"  ;0814000222;\n" // no name
	rows, errs, err := Parse("users.csv", []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Phone != "628123456789" || rows[0].Role != "" ||
		rows[1].Phone != "62811222333" || rows[1].Role != "admin" || rows[1].Line != 3 {
		t.Errorf("rows = %+v", rows)
	}
	if len(errs) != 4 {
		t.Fatalf("errs = %+v", errs)
	}
	for _, want := range []string{"nomor tidak valid", "sama dengan baris 2", "role harus", "nama kosong"} {
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Msg, want)
		}
		if !found {
			t.Errorf("missing error %q in %+v", want, errs)
		}
	}
}

func TestParseCSVComma(t *testing.T) {
	rows, errs, err := Parse("u.csv", []byte("phone,name\n08123456789,\"Budi, S.\"\n"))
	if err != nil || len(errs) != 0 || len(rows) != 1 || rows[0].Name != "Budi, S." {
		t.Fatalf("rows=%+v errs=%+v err=%v", rows, errs, err)
	}
}

func TestParseXLSX(t *testing.T) {
	f := excelize.NewFile()
	sh := f.GetSheetName(0)
	for i, row := range [][]any{
		{"No HP", "Nama Lengkap", "Peran"},
		{"8123456789", "Budi", "worker"}, // leading 0 lost by Excel
		{"6.28111222333E+11", "Sari", "admin"},
	} {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(sh, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	rows, errs, err := Parse("users.XLSX", buf.Bytes())
	if err != nil || len(errs) != 0 || len(rows) != 2 {
		t.Fatalf("rows=%+v errs=%+v err=%v", rows, errs, err)
	}
	if rows[0].Phone != "628123456789" || rows[1].Phone != "628111222333" || rows[1].Role != "admin" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no header":        "Budi,0812345678\nSari,0813456789\n",
		"empty":            "",
		"missing name col": "telepon,role\n0812345678,admin\n",
	} {
		if _, _, err := Parse("x.csv", []byte(in)); err != ErrNoHeader {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, _, err := Parse("x.pdf", nil); err == nil {
		t.Error("pdf should be rejected")
	}
}
