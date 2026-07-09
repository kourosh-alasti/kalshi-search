package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/state"
)

func TestApplySeriesDiversity(t *testing.T) {
	candidates := []candidate{
		{event: kalshi.Event{SeriesTicker: "A"}, score: 0.9},
		{event: kalshi.Event{SeriesTicker: "A"}, score: 0.8},
		{event: kalshi.Event{SeriesTicker: "A"}, score: 0.7},
		{event: kalshi.Event{SeriesTicker: "B"}, score: 0.6},
	}
	out := applySeriesDiversity(candidates, 2)
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
}

func TestMarketURL(t *testing.T) {
	url := marketURL(kalshi.Event{SeriesTicker: "KXNFL"}, kalshi.Market{Ticker: "KXNFL-24-BAL"})
	if url == "" || url == "https://kalshi.com/markets/kxnfl" {
		t.Fatalf("expected deep link, got %s", url)
	}
}

func TestDisqualifyPassDecayAndRealert(t *testing.T) {
	now := time.Now()
	filters := userFilters{maxPrice: 30, minVolume: 100, minOpenInterest: 50, closeWithin: 72 * time.Hour}
	alerted := map[string]*state.AlertedMarket{
		"T1": {Ticker: "T1", LastPriceCents: 20, Suppressed: true, SuppressedUntil: now.Add(24 * time.Hour)},
		"T2": {Ticker: "T2", LastPriceCents: 20, LastVolume: 1000, LastCloseHours: 48},
	}
	s := &Scanner{cfg: &config.Config{MaxPriceCents: 30, RealertDropCents: 5}}

	m := kalshi.Market{Ticker: "T1", Status: "active", YesAsk: 15, Volume: 5000, OpenInterest: 1000,
		CloseTime: now.Add(10 * time.Hour)}
	if r := s.disqualify(context.Background(), "p", m, kalshi.Event{Category: "Sports"}, now,
		map[string]bool{"sports": true}, nil, filters, alerted, nil, false, nil); r != "passed" {
		t.Fatalf("expected passed, got %s", r)
	}

	m.Ticker = "T2"
	m.Volume = 3000
	if r := s.disqualify(context.Background(), "p", m, kalshi.Event{Category: "Sports"}, now,
		map[string]bool{"sports": true}, nil, filters, alerted, nil, false, nil); r != "" {
		t.Fatalf("expected realert on volume spike, got %s", r)
	}
}

func TestFiltersForUser(t *testing.T) {
	s := &Scanner{cfg: &config.Config{
		MaxPriceCents: 30, MinVolume: 1000, MinOpenInterest: 500, CloseWithinHours: 72,
	}}
	max := 20
	f := s.filtersForUser(state.FilterOverrides{MaxPriceCents: &max})
	if f.maxPrice != 20 || f.minVolume != 1000 {
		t.Fatalf("unexpected filters: %+v", f)
	}
}

func TestInQuietHoursIntegration(t *testing.T) {
	q := state.QuietHours{Enabled: true, StartHour: 22, EndHour: 8, Timezone: "UTC"}
	// 23:00 UTC is quiet
	now := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	if !state.InQuietHours(q, now) {
		t.Fatal("expected quiet")
	}
}
