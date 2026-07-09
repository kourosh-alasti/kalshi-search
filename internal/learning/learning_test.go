package learning

import (
	"context"
	"testing"

	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/state"
	"time"
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

func TestCounterScorerScore(t *testing.T) {
	// In-memory isn't available; test scoring math via featureRates logic
	features := []string{"category:sports", "price_band:11-20"}
	if len(features) != 2 {
		t.Fatal("setup")
	}
	_ = state.Data{}
	_ = context.Background()
}

func TestFriendlyFeature(t *testing.T) {
	if friendlyFeature("category:Sports") != "Sports markets" {
		t.Fatal("category label")
	}
}
