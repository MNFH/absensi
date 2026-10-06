package nlu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func wrap(payload string) []byte {
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": payload}}}}},
	})
	return b
}

func TestParseResponse(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	tests := []struct {
		name    string
		payload string
		want    Intent
		wantErr bool
	}{
		{"unknown status", `{"action":"checkin","status":"unknown","time":""}`, Intent{Action: ActionCheckIn}, false},
		{"checkin wfo", `{"action":"checkin","status":"wfo","time":""}`, Intent{Action: ActionCheckIn, Status: StatusWFO}, false},
		{"checkout with time", `{"action":"checkout","status":"","time":"2026-01-05T17:00"}`,
			Intent{Action: ActionCheckOut, At: time.Date(2026, 1, 5, 17, 0, 0, 0, loc)}, false},
		{"unknown action becomes other", `{"action":"drop tables","status":"","time":""}`, Intent{Action: ActionOther}, false},
		{"unknown status dropped", `{"action":"checkin","status":"hybrid","time":""}`, Intent{Action: ActionCheckIn}, false},
		{"time ignored for other", `{"action":"other","status":"","time":"2026-01-05T17:00"}`, Intent{Action: ActionOther}, false},
		{"bad time", `{"action":"checkin","status":"","time":"besok"}`, Intent{}, true},
		{"not json", `hello`, Intent{}, true},
	}
	for _, tc := range tests {
		got, err := parseResponse(wrap(tc.payload), loc)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if err == nil && (got.Action != tc.want.Action || got.Status != tc.want.Status || !got.At.Equal(tc.want.At)) {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	if _, err := parseResponse([]byte(`{"candidates":[]}`), loc); err == nil {
		t.Error("empty candidates should fail")
	}
}

func TestExtractRequest(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	var gotKey, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("x-goog-api-key"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write(wrap(`{"action":"checkin","status":"wfh","time":""}`))
	}))
	defer srv.Close()

	c := New("k", "m1")
	c.BaseURL = srv.URL
	in, err := c.Extract(context.Background(), "baru sampai, wfh", time.Date(2026, 1, 5, 9, 0, 0, 0, loc))
	if err != nil || in.Action != ActionCheckIn || in.Status != StatusWFH {
		t.Fatalf("in=%+v err=%v", in, err)
	}
	if gotKey != "k" || gotPath != "/models/m1:generateContent" {
		t.Errorf("key=%q path=%q", gotKey, gotPath)
	}
	if !strings.Contains(gotBody, "2026-01-05T09:00") || !strings.Contains(gotBody, "baru sampai") {
		t.Errorf("body missing current time or text: %s", gotBody)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "quota", 429) }))
	defer bad.Close()
	c.BaseURL = bad.URL
	if _, err := c.Extract(context.Background(), "x", time.Now()); err == nil {
		t.Error("HTTP 429 should be an error")
	}
}

// Gemini rejects empty strings inside "enum"; keep the schema free of them.
func TestSchemaHasNoEmptyEnum(t *testing.T) {
	for name, p := range schema["properties"].(map[string]any) {
		if e, ok := p.(map[string]any)["enum"].([]string); ok {
			for _, v := range e {
				if v == "" {
					t.Errorf("property %s has an empty enum value", name)
				}
			}
		}
	}
}

func TestFallbackModels(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "busy"):
			http.Error(w, "high demand", http.StatusServiceUnavailable)
		case strings.Contains(r.URL.Path, "quota"):
			http.Error(w, "quota", http.StatusTooManyRequests)
		case strings.Contains(r.URL.Path, "bad"):
			http.Error(w, "invalid schema", http.StatusBadRequest)
		default:
			_, _ = w.Write(wrap(`{"action":"checkout","status":"unknown","time":""}`))
		}
	}))
	defer srv.Close()

	c := New("k", " busy , quota,good ")
	c.BaseURL = srv.URL
	in, err := c.Extract(context.Background(), "pulang", time.Now().In(loc))
	if err != nil || in.Action != ActionCheckOut || len(calls) != 3 {
		t.Fatalf("in=%+v err=%v calls=%v", in, err, calls)
	}

	calls = nil
	c = New("k", "bad,good")
	c.BaseURL = srv.URL
	if _, err := c.Extract(context.Background(), "pulang", time.Now()); err == nil || len(calls) != 1 {
		t.Errorf("400 must not fall back: err=%v calls=%v", err, calls)
	}

	calls = nil
	c = New("k", "busy,quota")
	c.BaseURL = srv.URL
	if _, err := c.Extract(context.Background(), "pulang", time.Now()); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Errorf("all failing: err=%v", err)
	}
}

func TestParseSummary(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, loc) }
	payload := func(start, end, person string) string {
		return `{"action":"summary","status":"unknown","time":"","period_start":"` + start + `","period_end":"` + end + `","person":"` + person + `"}`
	}

	in, err := parseResponse(wrap(payload("2026-09-01", "2026-09-30", "")), loc)
	if err != nil || in.Action != ActionSummary || !in.From.Equal(d(9, 1)) || !in.To.Equal(d(9, 30)) || in.Person != "" {
		t.Errorf("month: %+v %v", in, err)
	}
	in, err = parseResponse(wrap(payload("", "", " fajar ")), loc)
	if err != nil || !in.From.IsZero() || in.Person != "fajar" {
		t.Errorf("person only: %+v %v", in, err)
	}
	for name, p := range map[string]string{
		"reversed":   payload("2026-09-30", "2026-09-01", ""),
		"half":       payload("2026-09-01", "", ""),
		"bad format": payload("September", "Oktober", ""),
	} {
		if _, err := parseResponse(wrap(p), loc); err == nil {
			t.Errorf("%s should fail", name)
		}
	}
	// Check-in payloads ignore summary fields.
	in, err = parseResponse(wrap(`{"action":"checkin","status":"wfo","time":"","period_start":"2026-09-01","period_end":"2026-09-30","person":"x"}`), loc)
	if err != nil || in.Action != ActionCheckIn || !in.From.IsZero() || in.Person != "" {
		t.Errorf("checkin with stray fields: %+v %v", in, err)
	}
}
