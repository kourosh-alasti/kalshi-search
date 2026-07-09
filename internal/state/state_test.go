package state

import (
	"strconv"
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

func TestInQuietHoursSameDay(t *testing.T) {
	q := QuietHours{Enabled: true, StartHour: 9, EndHour: 17, Timezone: "UTC"}
	cases := []struct {
		hour  int
		quiet bool
	}{
		{8, false},
		{9, true},
		{12, true},
		{16, true},
		{17, false},
		{20, false},
	}
	for _, tc := range cases {
		now := time.Date(2026, 6, 1, tc.hour, 0, 0, 0, time.UTC)
		if got := InQuietHours(q, now); got != tc.quiet {
			t.Fatalf("hour %d: got %v want %v", tc.hour, got, tc.quiet)
		}
	}
}

func TestPruneSuggestions(t *testing.T) {
	d := &Data{Suggestions: map[string]*Suggestion{}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 250; i++ {
		label := LabelTook
		if i > 200 {
			label = LabelPending
		}
		d.Suggestions[strconv.Itoa(i)] = &Suggestion{
			ID: i, Label: label, SentAt: base.Add(time.Duration(i) * time.Hour),
		}
	}
	pruneSuggestions(d, 200)
	if len(d.Suggestions) > 200 {
		t.Fatalf("expected <=200 suggestions, got %d", len(d.Suggestions))
	}
	for i := 201; i <= 250; i++ {
		if _, ok := d.Suggestions[strconv.Itoa(i)]; !ok {
			t.Fatalf("pending suggestion %d should remain", i)
		}
	}
}
