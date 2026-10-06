package db

import (
	"errors"
	"testing"
)

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"08123456789":      "628123456789",
		"0812-3456-789":    "628123456789",
		"+62 812 3456 789": "628123456789",
		"628123456789":     "628123456789",
		"8123456789":       "628123456789",
		"(0812) 3456.789":  "628123456789",
		"+1 415 555 0100":  "14155550100",
	}
	for in, want := range ok {
		if got, err := NormalizePhone(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "0812abc", "123", "0812345678901234567"} {
		if _, err := NormalizePhone(in); !errors.Is(err, ErrBadPhone) {
			t.Errorf("%q: err = %v, want ErrBadPhone", in, err)
		}
	}
}
