package state

import (
	"testing"
	"time"
)

func TestInQuietHours(t *testing.T) {
	q := QuietHours{Enabled: true, StartHour: 22, EndHour: 8, Timezone: "UTC"}

	cases := []struct {
		hour  int
		quiet bool
	}{
		{21, false},
		{22, true},
		{23, true},
		{3, true},
		{7, true},
		{8, false},
		{12, false},
	}
	for _, tc := range cases {
		now := time.Date(2026, 6, 1, tc.hour, 30, 0, 0, time.UTC)
		got := InQuietHours(q, now)
		if got != tc.quiet {
			t.Fatalf("hour %d: got %v want %v", tc.hour, got, tc.quiet)
		}
	}
}

func TestInQuietHoursDisabled(t *testing.T) {
	q := DefaultQuietHours()
	now := time.Now()
	if InQuietHours(q, now) {
		t.Fatal("disabled quiet hours should never match")
	}
}

func TestDefaultQuietHours(t *testing.T) {
	q := DefaultQuietHours()
	if q.StartHour != 22 || q.EndHour != 8 {
		t.Fatal("unexpected defaults")
	}
}
