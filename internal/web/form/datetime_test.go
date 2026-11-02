package form

import (
	"testing"
	"time"
)

func TestParseLocalDateTime(t *testing.T) {
	for raw, want := range map[string]bool{"": true, "2026-11-02T09:30": true, "2026-11-02T09:30:15": true, "1969-12-31T23:59": false, "2026-11-02": false, "x": false} {
		if _, ok := ParseLocalDateTime(raw); ok != want {
			t.Errorf("%q: ok=%v, want %v", raw, ok, want)
		}
	}
	got, _ := ParseLocalDateTime("2026-11-02T09:30")
	if want := time.Date(2026, 11, 2, 9, 30, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if FormatLocalDateTime(got) != "2026-11-02T09:30" || FormatLocalDateTime(time.Time{}) != "" {
		t.Error("format")
	}
}
