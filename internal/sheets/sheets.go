// Package sheets implements attendance.Store on a Google Spreadsheet with one
// tab per month, named YYYY-MM.
package sheets

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/option"
	gs "google.golang.org/api/sheets/v4"

	"github.com/absensi/internal/attendance"
)

var header = []any{"Waktu Dicatat", "Tanggal", "Jam", "Telegram ID", "Nama", "Telepon", "Tipe", "Status",
	"Latitude", "Longitude", "Alamat", "Peta", "Sumber"}

// Column positions (0-based) matching header.
const (
	colRecorded = iota
	colDate
	colTime
	colID
	colName
	colPhone
	colType
	colStatus
	colLat
	colLng
	colAddress
	colMap
	colSource
	numCols
)

const (
	dateLayout     = "2006-01-02"
	timeLayout     = "15:04:05"
	recordedLayout = "2006-01-02 15:04:05"
)

type Client struct {
	svc *gs.Service
	id  string
	loc *time.Location

	mu   sync.Mutex
	tabs map[string]bool // known existing tab titles
}

func New(ctx context.Context, credsFile, spreadsheetID string, loc *time.Location) (*Client, error) {
	svc, err := gs.NewService(ctx, option.WithCredentialsFile(credsFile), option.WithScopes(gs.SpreadsheetsScope))
	if err != nil {
		return nil, err
	}
	c := &Client{svc: svc, id: spreadsheetID, loc: loc, tabs: map[string]bool{}}
	if err := c.refreshTabs(ctx); err != nil {
		return nil, fmt.Errorf("open spreadsheet (is it shared with the service account?): %w", err)
	}
	return c, nil
}

func (c *Client) refreshTabs(ctx context.Context) error {
	ss, err := c.svc.Spreadsheets.Get(c.id).Fields("sheets.properties.title").Context(ctx).Do()
	if err != nil {
		return err
	}
	c.tabs = map[string]bool{}
	for _, s := range ss.Sheets {
		c.tabs[s.Properties.Title] = true
	}
	return nil
}

// ensureTab creates the tab and header if needed. Caller must hold c.mu.
func (c *Client) ensureTab(ctx context.Context, title string) error {
	if c.tabs[title] {
		return nil
	}
	_, err := c.svc.Spreadsheets.BatchUpdate(c.id, &gs.BatchUpdateSpreadsheetRequest{
		Requests: []*gs.Request{{AddSheet: &gs.AddSheetRequest{
			Properties: &gs.SheetProperties{Title: title, GridProperties: &gs.GridProperties{FrozenRowCount: 1}},
		}}},
	}).Context(ctx).Do()
	if err != nil {
		// Someone else may have created it; re-sync and re-check.
		if rerr := c.refreshTabs(ctx); rerr != nil || !c.tabs[title] {
			return err
		}
		return nil
	}
	c.tabs[title] = true
	_, err = c.svc.Spreadsheets.Values.Update(c.id, quote(title)+"!A1:M1",
		&gs.ValueRange{Values: [][]any{header}}).ValueInputOption("RAW").Context(ctx).Do()
	return err
}

func quote(title string) string { return "'" + title + "'" }

func (c *Client) toRow(r attendance.Record) []any {
	row := make([]any, numCols)
	row[colRecorded] = r.RecordedAt.In(c.loc).Format(recordedLayout)
	row[colDate] = r.At.In(c.loc).Format(dateLayout)
	row[colTime] = r.At.In(c.loc).Format(timeLayout)
	row[colID] = strconv.FormatInt(r.TelegramID, 10)
	row[colName] = r.Name
	row[colPhone] = r.Phone
	row[colType] = r.Type
	row[colStatus] = r.Status
	row[colSource] = r.Source
	for _, i := range []int{colLat, colLng, colAddress, colMap} {
		row[i] = ""
	}
	if r.HasLocation {
		row[colLat] = strconv.FormatFloat(r.Lat, 'f', 6, 64)
		row[colLng] = strconv.FormatFloat(r.Lng, 'f', 6, 64)
		row[colAddress] = r.Address
		row[colMap] = fmt.Sprintf("https://www.google.com/maps?q=%.6f,%.6f", r.Lat, r.Lng)
	}
	return row
}

func (c *Client) Append(ctx context.Context, r attendance.Record) error {
	return c.AppendMany(ctx, []attendance.Record{r})
}

// AppendMany writes records with one API call per month tab (the Sheets API
// allows ~60 writes/minute, so bulk loads must not go row by row).
func (c *Client) AppendMany(ctx context.Context, recs []attendance.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	byTab := map[string][][]any{}
	var order []string
	for _, r := range recs {
		title := r.At.In(c.loc).Format("2006-01")
		if _, ok := byTab[title]; !ok {
			order = append(order, title)
		}
		byTab[title] = append(byTab[title], c.toRow(r))
	}
	for _, title := range order {
		if err := c.ensureTab(ctx, title); err != nil {
			return err
		}
		_, err := c.svc.Spreadsheets.Values.Append(c.id, quote(title)+"!A:M", &gs.ValueRange{Values: byTab[title]}).
			ValueInputOption("RAW").InsertDataOption("INSERT_ROWS").Context(ctx).Do()
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteWhere removes rows matching fn from the month tabs overlapping
// [from, to) by rewriting each tab's data range. Not safe to run while the
// bot is writing to the same tab; stop the bot first.
func (c *Client) DeleteWhere(ctx context.Context, from, to time.Time, fn func(attendance.Record) bool) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refreshTabs(ctx); err != nil {
		return 0, err
	}
	removed := 0
	for m := time.Date(from.In(c.loc).Year(), from.In(c.loc).Month(), 1, 0, 0, 0, 0, c.loc); m.Before(to); m = m.AddDate(0, 1, 0) {
		title := m.Format("2006-01")
		if !c.tabs[title] {
			continue
		}
		resp, err := c.svc.Spreadsheets.Values.Get(c.id, quote(title)+"!A2:M").Context(ctx).Do()
		if err != nil {
			return removed, err
		}
		var keep [][]any
		for _, row := range resp.Values {
			if rec, ok := c.parseRow(row); ok && fn(rec) {
				removed++
				continue
			}
			keep = append(keep, row)
		}
		if len(keep) == len(resp.Values) {
			continue
		}
		if _, err := c.svc.Spreadsheets.Values.Clear(c.id, quote(title)+"!A2:M", &gs.ClearValuesRequest{}).Context(ctx).Do(); err != nil {
			return removed, err
		}
		if len(keep) > 0 {
			if _, err := c.svc.Spreadsheets.Values.Update(c.id, quote(title)+"!A2", &gs.ValueRange{Values: keep}).
				ValueInputOption("RAW").Context(ctx).Do(); err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

func (c *Client) Read(ctx context.Context, from, to time.Time) ([]attendance.Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []attendance.Record
	// Iterate month tabs overlapping [from, to).
	m := time.Date(from.In(c.loc).Year(), from.In(c.loc).Month(), 1, 0, 0, 0, 0, c.loc)
	for m.Before(to) {
		title := m.Format("2006-01")
		m = m.AddDate(0, 1, 0)
		if !c.tabs[title] {
			// A tab may have been added by hand since startup.
			if err := c.refreshTabs(ctx); err != nil {
				return nil, err
			}
			if !c.tabs[title] {
				continue
			}
		}
		resp, err := c.svc.Spreadsheets.Values.Get(c.id, quote(title)+"!A2:M").Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		for _, row := range resp.Values {
			rec, ok := c.parseRow(row)
			if ok && !rec.At.Before(from) && rec.At.Before(to) {
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

func (c *Client) parseRow(row []any) (attendance.Record, bool) {
	if len(row) <= colType {
		return attendance.Record{}, false
	}
	s := func(i int) string {
		if i >= len(row) {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(row[i]))
	}
	at, err := time.ParseInLocation(dateLayout+" "+timeLayout, s(colDate)+" "+s(colTime), c.loc)
	if err != nil {
		return attendance.Record{}, false
	}
	id, err := strconv.ParseInt(s(colID), 10, 64)
	if err != nil {
		return attendance.Record{}, false
	}
	recorded, _ := time.ParseInLocation(recordedLayout, s(colRecorded), c.loc)
	typ := strings.ToUpper(s(colType))
	if typ != attendance.TypeIn && typ != attendance.TypeOut {
		return attendance.Record{}, false
	}
	rec := attendance.Record{
		At: at, RecordedAt: recorded, TelegramID: id, Name: s(colName), Phone: s(colPhone), Type: typ,
		Status: strings.ToUpper(s(colStatus)), Address: s(colAddress), Source: s(colSource),
	}
	lat, e1 := strconv.ParseFloat(s(colLat), 64)
	lng, e2 := strconv.ParseFloat(s(colLng), 64)
	if e1 == nil && e2 == nil {
		rec.HasLocation, rec.Lat, rec.Lng = true, lat, lng
	}
	return rec, true
}
