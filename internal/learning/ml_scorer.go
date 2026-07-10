package learning

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// MLScorer trains a per-user logistic model from suggestion_records.
type MLScorer struct {
	db          *sql.DB
	minExamples int
	logger      *slog.Logger

	mu    sync.Mutex
	cache map[string]*mlCacheEntry
}

type mlCacheEntry struct {
	model     *logisticModel
	examples  int
	trainedAt time.Time
}

// NewMLScorer returns an ML scorer backed by suggestion_records.
func NewMLScorer(db *sql.DB, minExamples int, logger *slog.Logger) *MLScorer {
	if minExamples < 1 {
		minExamples = 8
	}
	return &MLScorer{
		db: db, minExamples: minExamples,
		logger: logger.With("component", "ml_scorer"),
		cache:  map[string]*mlCacheEntry{},
	}
}

// Invalidate drops the cached model for a user after new feedback.
func (m *MLScorer) Invalidate(phone string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, phone)
}

func (m *MLScorer) modelFor(ctx context.Context, phone string) (*logisticModel, int, bool) {
	m.mu.Lock()
	if ent, ok := m.cache[phone]; ok && time.Since(ent.trainedAt) < 5*time.Minute {
		m.mu.Unlock()
		return ent.model, ent.examples, ent.model != nil && ent.examples >= m.minExamples
	}
	m.mu.Unlock()

	examples, err := LoadTrainingExamples(ctx, m.db, phone, m.logger)
	if err != nil {
		m.logger.Warn("loading training data failed", "error", err, "phone", phone)
		return nil, 0, false
	}
	if len(examples) < m.minExamples {
		ent := &mlCacheEntry{examples: len(examples), trainedAt: time.Now()}
		m.mu.Lock()
		m.cache[phone] = ent
		m.mu.Unlock()
		return nil, len(examples), false
	}

	model := trainLogistic(examples, 80, 0.12)
	ent := &mlCacheEntry{model: model, examples: len(examples), trainedAt: time.Now()}

	m.mu.Lock()
	m.cache[phone] = ent
	m.mu.Unlock()
	return model, len(examples), true
}

// ScoreAndExplain returns ML score and rationale, or -1 when insufficient data.
func (m *MLScorer) ScoreAndExplain(ctx context.Context, phone string, features []string) (float64, string) {
	model, n, ready := m.modelFor(ctx, phone)
	if !ready {
		return -1, fmt.Sprintf("need %d+ labeled picks (%d so far)", m.minExamples, n)
	}
	score := model.predictFeatures(features)
	top := model.topFeatures(features, 2)
	if len(top) == 0 {
		return score, fmt.Sprintf("enhanced match (%.0f%%)", score*100)
	}
	parts := make([]string, 0, len(top))
	for _, tf := range top {
		parts = append(parts, friendlyFeature(tf.feature))
	}
	return score, fmt.Sprintf("enhanced %.0f%% — driven by %s", score*100, joinParts(parts))
}

// Score returns ML accept probability in [0,1], or -1 when insufficient data.
func (m *MLScorer) Score(ctx context.Context, phone string, features []string) float64 {
	score, _ := m.ScoreAndExplain(ctx, phone, features)
	return score
}

// Explain returns a short rationale from the ML model weights.
func (m *MLScorer) Explain(ctx context.Context, phone string, features []string) string {
	_, explain := m.ScoreAndExplain(ctx, phone, features)
	return explain
}

func joinParts(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return parts[0] + " + " + parts[1]
	}
}
