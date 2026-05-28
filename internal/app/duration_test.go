package app_test

import (
	"testing"
	"time"

	"kitsoki/internal/app"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"  ", 0, false},
		{"10s", 10 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"1d", 24 * time.Hour, false},
		{"10d", 240 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"1d2h", 0, true},  // mixed units rejected
		{"d", 0, true},     // missing count
		{"abc", 0, true},   // garbage
		{"10ds", 0, true},  // garbage suffix
		{"10x", 0, true},   // unknown unit
	}
	for _, c := range cases {
		got, err := app.ParseDuration(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseDuration(%q): want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDuration(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
