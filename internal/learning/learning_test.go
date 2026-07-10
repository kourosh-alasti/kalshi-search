package learning

import (
	"testing"
	"time"

	"github.com/kourosh/kalshi-search/internal/kalshi"
)

func TestExtractFeaturesRich(t *testing.T) {
	now := time.Now()
	ev := kalshi.Event{Category: "Sports", SeriesTicker: "KXNFL"}
	m := kalshi.Market{
		YesAsk: 15, YesBid: 12, Volume: 10000, Volume24H: 2000,
		CloseTime: now.Add(12 * time.Hour),
	}
	feats := ExtractFeatures(ev, m, now, FeatureContext{PrevPriceCents: 20})
	if len(feats) < 7 {
		t.Fatalf("expected rich features, got %v", feats)
	}
}

func TestExplainFromContribs(t *testing.T) {
	contribs := []FeatureContribution{
		{Feature: "category:sports", Rate: 0.8},
		{Feature: "price_band:11-20", Rate: 0.5},
	}
	msg := explainFromContribs(contribs, 0.65)
	if msg == "" || msg == "neutral match (65%)" {
		t.Fatalf("expected strong-category explanation, got %q", msg)
	}
}

func TestFeatureRateMath(t *testing.T) {
	accepts, rejects := 3, 1
	rate := (float64(accepts) + 1) / (float64(accepts+rejects) + 2)
	if rate < 0.66 || rate > 0.68 {
		t.Fatalf("unexpected laplace rate: %f", rate)
	}
}

func TestFriendlyFeature(t *testing.T) {
	if friendlyFeature("category:Sports") != "Sports markets" {
		t.Fatal("category label")
	}
}
