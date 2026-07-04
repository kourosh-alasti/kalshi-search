package learning

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// SuggestionRecord is one training-set row for an ML scorer. Label rows
// (kind "label") are written when feedback arrives, joined to suggestion rows
// by suggestion_id.
type SuggestionRecord struct {
	Kind         string    `json:"kind"` // "suggestion" or "label"
	Timestamp    time.Time `json:"timestamp"`
	SuggestionID int       `json:"suggestion_id"`
	Ticker       string    `json:"ticker,omitempty"`
	Title        string    `json:"title,omitempty"`
	Category     string    `json:"category,omitempty"`
	SeriesTicker string    `json:"series_ticker,omitempty"`
	YesAsk       int       `json:"yes_ask,omitempty"`
	YesBid       int       `json:"yes_bid,omitempty"`
	Volume       int       `json:"volume,omitempty"`
	OpenInterest int       `json:"open_interest,omitempty"`
	CloseTime    time.Time `json:"close_time,omitzero"`
	Features     []string  `json:"features,omitempty"`
	Score        float64   `json:"score,omitempty"`
	Label        string    `json:"label,omitempty"` // took | pass | position
}

// SuggestionLog persists suggestion and label records in libSQL.
type SuggestionLog struct {
	mu     sync.Mutex
	db     *sql.DB
	logger *slog.Logger
}

// NewSuggestionLog creates the log writer backed by libSQL.
func NewSuggestionLog(db *sql.DB, logger *slog.Logger) *SuggestionLog {
	return &SuggestionLog{db: db, logger: logger.With("component", "suggestion_log")}
}

// Append writes one record for the given user phone.
func (l *SuggestionLog) Append(ctx context.Context, phone string, rec SuggestionRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var featuresJSON sql.NullString
	if len(rec.Features) > 0 {
		raw, err := json.Marshal(rec.Features)
		if err != nil {
			l.logger.Error("encoding suggestion features failed", "error", err)
			return
		}
		featuresJSON = sql.NullString{String: string(raw), Valid: true}
	}

	var closeTime sql.NullString
	if !rec.CloseTime.IsZero() {
		closeTime = sql.NullString{String: rec.CloseTime.UTC().Format(time.RFC3339Nano), Valid: true}
	}

	_, err := l.db.ExecContext(ctx, `
		INSERT INTO suggestion_records (
			phone, kind, recorded_at, suggestion_id, ticker, title, category,
			series_ticker, yes_ask, yes_bid, volume, open_interest, close_time,
			features, score, label
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		phone, rec.Kind, rec.Timestamp.UTC().Format(time.RFC3339Nano), rec.SuggestionID,
		nullString(rec.Ticker), nullString(rec.Title), nullString(rec.Category),
		nullString(rec.SeriesTicker), nullInt(rec.YesAsk), nullInt(rec.YesBid),
		nullInt(rec.Volume), nullInt(rec.OpenInterest), closeTime, featuresJSON,
		nullFloat(rec.Score), nullString(rec.Label))
	if err != nil {
		l.logger.Error("writing suggestion record failed", "error", err, "phone", phone)
	}
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

func nullFloat(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}
