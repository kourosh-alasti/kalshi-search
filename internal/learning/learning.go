// Package learning implements the preference model that ranks candidate
// trades based on the user's past accept/reject decisions.
//
// The initial implementation is a transparent counter-based scorer: each
// market decomposes into categorical features, each feature accumulates
// accept/reject counts, and a market's score is the mean of its features'
// Laplace-smoothed accept rates. The Scorer interface is the seam for a
// future ML-based implementation.
package learning

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/state"
)

// Scorer scores candidate markets and consumes feedback. Implementations
// must be safe to call from a single goroutine (the scanner loop).
type Scorer interface {
	// Score returns a preference score in [0, 1]; higher = more like trades
	// the user has taken. Neutral prior is 0.5.
	Score(ctx context.Context, phone string, features []string) float64
	// Feed records one accept/reject decision for a set of features.
	Feed(ctx context.Context, phone string, features []string, accepted bool)
}

// ExtractFeatures decomposes a market (and its parent event) into the
// categorical features used for preference learning.
func ExtractFeatures(event kalshi.Event, market kalshi.Market, now time.Time) []string {
	return []string{
		"category:" + strings.ToLower(event.Category),
		"series:" + event.SeriesTicker,
		"price_band:" + priceBand(market.YesAsk),
		"close_bucket:" + closeBucket(market.CloseTime.Sub(now)),
		"volume_bucket:" + volumeBucket(market.Volume),
	}
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

// CounterScorer is the default Scorer backed by per-user feature stats in
// the state store.
type CounterScorer struct {
	store  *state.Store
	logger *slog.Logger
}

// NewCounterScorer returns a scorer reading and writing feature stats in the
// given store.
func NewCounterScorer(store *state.Store, logger *slog.Logger) *CounterScorer {
	return &CounterScorer{store: store, logger: logger.With("component", "learning")}
}

// Score computes the mean Laplace-smoothed accept rate across features.
// A feature with no history contributes the neutral prior 0.5.
func (s *CounterScorer) Score(ctx context.Context, phone string, features []string) float64 {
	if len(features) == 0 {
		return 0.5
	}
	var total float64
	var contributions []string
	_ = s.store.View(ctx, phone, func(d *state.Data) {
		for _, f := range features {
			rate := 0.5
			if st, ok := d.Features[f]; ok {
				rate = (float64(st.Accepts) + 1) / (float64(st.Accepts+st.Rejects) + 2)
			}
			total += rate
			contributions = append(contributions, fmt.Sprintf("%s=%.2f", f, rate))
		}
	})
	score := total / float64(len(features))
	s.logger.Debug("scored market", "phone", phone, "score", fmt.Sprintf("%.3f", score), "contributions", contributions)
	return score
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

// SeriesBoost reports whether a series has been explicitly taken at least
// twice — used to expand alerts beyond enabled categories when
// LEARN_EXPAND_CATEGORIES is on.
func (s *CounterScorer) SeriesBoost(ctx context.Context, phone, seriesTicker string) bool {
	boost := false
	_ = s.store.View(ctx, phone, func(d *state.Data) {
		if st, ok := d.Features["series:"+seriesTicker]; ok {
			boost = st.Accepts >= 2
		}
	})
	return boost
}
