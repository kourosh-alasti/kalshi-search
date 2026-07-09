// Package learning implements the preference model that ranks candidate
// trades based on the user's past accept/reject decisions.
package learning

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/metrics"
	"github.com/kourosh/kalshi-search/internal/state"
)

// Scorer scores candidate markets and consumes feedback.
type Scorer interface {
	Score(ctx context.Context, phone string, features []string) float64
	Feed(ctx context.Context, phone string, features []string, accepted bool)
}

// FeatureContext carries optional market context for richer features.
type FeatureContext struct {
	PrevPriceCents int
	HasPosition    bool
}

// ExtractFeatures decomposes a market into categorical learning features.
func ExtractFeatures(event kalshi.Event, market kalshi.Market, now time.Time, ctx FeatureContext) []string {
	feats := []string{
		"category:" + strings.ToLower(event.Category),
		"series:" + event.SeriesTicker,
		"price_band:" + priceBand(market.YesAsk),
		"close_bucket:" + closeBucket(market.CloseTime.Sub(now)),
		"volume_bucket:" + volumeBucket(market.Volume),
		"spread_band:" + spreadBand(market.YesAsk, market.YesBid),
		"vol24h_bucket:" + volume24hBucket(market.Volume24H),
	}
	if ctx.PrevPriceCents > 0 && market.YesAsk < ctx.PrevPriceCents {
		feats = append(feats, "momentum:down")
	} else if ctx.PrevPriceCents > 0 && market.YesAsk > ctx.PrevPriceCents {
		feats = append(feats, "momentum:up")
	}
	if ctx.HasPosition {
		feats = append(feats, "held:yes")
	}
	return feats
}

func priceBand(cents int) string {
	switch {
	case cents <= 10:
		return "0-10"
	case cents <= 20:
		return "11-20"
	case cents <= 30:
		return "21-30"
	default:
		return "31+"
	}
}

func closeBucket(d time.Duration) string {
	switch {
	case d <= 6*time.Hour:
		return "<6h"
	case d <= 24*time.Hour:
		return "6-24h"
	case d <= 72*time.Hour:
		return "1-3d"
	default:
		return ">3d"
	}
}

func volumeBucket(v int) string {
	switch {
	case v < 5_000:
		return "<5k"
	case v < 50_000:
		return "5k-50k"
	default:
		return ">50k"
	}
}

func volume24hBucket(v int) string {
	switch {
	case v < 500:
		return "<500"
	case v < 5_000:
		return "500-5k"
	default:
		return ">5k"
	}
}

func spreadBand(ask, bid int) string {
	spread := ask - bid
	switch {
	case spread <= 2:
		return "tight"
	case spread <= 5:
		return "medium"
	default:
		return "wide"
	}
}

// FeatureContribution is one feature's smoothed accept rate in a score.
type FeatureContribution struct {
	Feature string
	Rate    float64
}

// CounterScorer is the default Scorer backed by per-user feature stats.
type CounterScorer struct {
	store  *state.Store
	logger *slog.Logger
}

// NewCounterScorer returns a scorer reading and writing feature stats in the store.
func NewCounterScorer(store *state.Store, logger *slog.Logger) *CounterScorer {
	return &CounterScorer{store: store, logger: logger.With("component", "learning")}
}

func (s *CounterScorer) featureRates(ctx context.Context, phone string, features []string) ([]FeatureContribution, float64, error) {
	if len(features) == 0 {
		return nil, 0.5, nil
	}
	var contribs []FeatureContribution
	var total float64
	err := s.store.View(ctx, phone, func(d *state.Data) {
		for _, f := range features {
			rate := 0.5
			if st, ok := d.Features[f]; ok {
				rate = (float64(st.Accepts) + 1) / (float64(st.Accepts+st.Rejects) + 2)
			}
			contribs = append(contribs, FeatureContribution{Feature: f, Rate: rate})
			total += rate
		}
	})
	if err != nil {
		return nil, 0.5, err
	}
	return contribs, total / float64(len(features)), nil
}

// Score computes the mean Laplace-smoothed accept rate across features.
func (s *CounterScorer) Score(ctx context.Context, phone string, features []string) float64 {
	contribs, score, err := s.featureRates(ctx, phone, features)
	if err != nil {
		s.logger.Warn("scoring failed", "error", err, "phone", phone)
		return 0.5
	}
	var parts []string
	for _, c := range contribs {
		parts = append(parts, fmt.Sprintf("%s=%.2f", c.Feature, c.Rate))
	}
	s.logger.Debug("scored market", "phone", phone, "score", fmt.Sprintf("%.3f", score), "contributions", parts)
	return score
}

// ScoreAndExplain returns score and rationale from a single store read.
func (s *CounterScorer) ScoreAndExplain(ctx context.Context, phone string, features []string) (float64, string) {
	contribs, score, err := s.featureRates(ctx, phone, features)
	if err != nil {
		s.logger.Warn("scoring failed", "error", err, "phone", phone)
		return 0.5, "neutral match (50%)"
	}
	var parts []string
	for _, c := range contribs {
		parts = append(parts, fmt.Sprintf("%s=%.2f", c.Feature, c.Rate))
	}
	s.logger.Debug("scored market", "phone", phone, "score", fmt.Sprintf("%.3f", score), "contributions", parts)
	return score, explainFromContribs(contribs, score)
}

// Explain returns a short human-readable rationale from top feature contributions.
func (s *CounterScorer) Explain(ctx context.Context, phone string, features []string) string {
	contribs, score, err := s.featureRates(ctx, phone, features)
	if err != nil {
		s.logger.Warn("explain failed", "error", err, "phone", phone)
		return "neutral match (50%)"
	}
	return explainFromContribs(contribs, score)
}

func explainFromContribs(contribs []FeatureContribution, score float64) string {
	if len(contribs) == 0 {
		return fmt.Sprintf("neutral match (%.0f%%)", score*100)
	}
	sort.Slice(contribs, func(i, j int) bool {
		deltaI := contribs[i].Rate - 0.5
		deltaJ := contribs[j].Rate - 0.5
		if deltaI < 0 {
			deltaI = -deltaI
		}
		if deltaJ < 0 {
			deltaJ = -deltaJ
		}
		return deltaI > deltaJ
	})
	top := contribs[0]
	label := friendlyFeature(top.Feature)
	if top.Rate >= 0.55 {
		return fmt.Sprintf("fits your %s pattern (%.0f%%)", label, top.Rate*100)
	}
	if top.Rate <= 0.45 {
		return fmt.Sprintf("weaker on %s (%.0f%%)", label, top.Rate*100)
	}
	return fmt.Sprintf("neutral %s (%.0f%%)", label, score*100)
}

func friendlyFeature(f string) string {
	parts := strings.SplitN(f, ":", 2)
	if len(parts) != 2 {
		return f
	}
	switch parts[0] {
	case "category":
		return parts[1] + " markets"
	case "series":
		return parts[1] + " series"
	case "price_band":
		return parts[1] + "¢ range"
	case "close_bucket":
		return parts[1] + " close"
	case "volume_bucket", "vol24h_bucket":
		return parts[1] + " volume"
	case "spread_band":
		return parts[1] + " spread"
	case "momentum":
		return "price " + parts[1]
	default:
		return parts[1]
	}
}

// Feed increments accept or reject counts for every feature.
func (s *CounterScorer) Feed(ctx context.Context, phone string, features []string, accepted bool) {
	err := s.store.Update(ctx, phone, func(d *state.Data) {
		for _, f := range features {
			st := d.Features[f]
			if st == nil {
				st = &state.FeatureStats{}
				d.Features[f] = st
			}
			if accepted {
				st.Accepts++
			} else {
				st.Rejects++
			}
		}
	})
	if err != nil {
		s.logger.Error("persisting feedback failed", "error", err, "phone", phone)
	}
	s.logger.Info("feedback recorded", "phone", phone, "accepted", accepted, "features", features)
}

// SeriesBoost reports whether a series has been explicitly taken at least twice.
func (s *CounterScorer) SeriesBoost(ctx context.Context, phone, seriesTicker string) bool {
	boost := false
	_ = s.store.View(ctx, phone, func(d *state.Data) {
		if st, ok := d.Features["series:"+seriesTicker]; ok {
			boost = st.Accepts >= 2
		}
	})
	return boost
}

// RecordFilterReason increments a Prometheus counter for filter telemetry.
func RecordFilterReason(reason string) {
	metrics.Register()
	metrics.FilterReasons.WithLabelValues(reason).Inc()
}
