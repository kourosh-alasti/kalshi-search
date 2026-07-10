package learning

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/kourosh/kalshi-search/internal/state"
)

// PreferenceScorer routes scoring to counter or ML models based on per-user opt-in.
type PreferenceScorer struct {
	store   *state.Store
	counter *CounterScorer
	ml      *MLScorer
	logger  *slog.Logger
}

// NewPreferenceScorer wires counter and ML scorers with per-user enhanced opt-in.
func NewPreferenceScorer(store *state.Store, db *sql.DB, minExamples int, logger *slog.Logger) *PreferenceScorer {
	return &PreferenceScorer{
		store:   store,
		counter: NewCounterScorer(store, logger),
		ml:      NewMLScorer(db, minExamples, logger),
		logger:  logger.With("component", "preference"),
	}
}

func (p *PreferenceScorer) enhancedEnabled(ctx context.Context, phone string) bool {
	enabled := false
	_ = p.store.View(ctx, phone, func(d *state.Data) {
		enabled = d.EnhancedSuggestions
	})
	return enabled
}

// Score implements Scorer using the active model for the user.
func (p *PreferenceScorer) Score(ctx context.Context, phone string, features []string) float64 {
	score, _ := p.ScoreAndExplain(ctx, phone, features)
	return score
}

// ScoreAndExplain scores with rationale, using ML when opted in and trained.
func (p *PreferenceScorer) ScoreAndExplain(ctx context.Context, phone string, features []string) (float64, string) {
	if !p.enhancedEnabled(ctx, phone) {
		return p.counter.ScoreAndExplain(ctx, phone, features)
	}
	mlScore := p.ml.Score(ctx, phone, features)
	if mlScore < 0 {
		score, explain := p.counter.ScoreAndExplain(ctx, phone, features)
		return score, "enhanced on (learning) — " + explain
	}
	return mlScore, p.ml.Explain(ctx, phone, features)
}

// Explain returns rationale for the active model.
func (p *PreferenceScorer) Explain(ctx context.Context, phone string, features []string) string {
	_, explain := p.ScoreAndExplain(ctx, phone, features)
	return explain
}

// Feed records feedback in the counter model and invalidates the ML cache.
func (p *PreferenceScorer) Feed(ctx context.Context, phone string, features []string, accepted bool) {
	p.counter.Feed(ctx, phone, features, accepted)
	p.ml.Invalidate(phone)
}

// SeriesBoost delegates to the counter scorer (category expansion signal).
func (p *PreferenceScorer) SeriesBoost(ctx context.Context, phone, seriesTicker string) bool {
	return p.counter.SeriesBoost(ctx, phone, seriesTicker)
}
