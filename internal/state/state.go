// Package state persists per-user mutable service state (category subscriptions,
// alert dedup, suggestion feedback, learned feature stats) in libSQL.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// SuggestionLabel is the feedback status of a suggestion.
type SuggestionLabel string

const (
	LabelPending  SuggestionLabel = "pending"
	LabelTook     SuggestionLabel = "took"     // explicit TOOK reply
	LabelPass     SuggestionLabel = "pass"     // explicit PASS reply
	LabelPosition SuggestionLabel = "position" // detected via portfolio positions
)

// Suggestion is one alert we texted, awaiting or holding feedback.
type Suggestion struct {
	ID         int             `json:"id"`
	Ticker     string          `json:"ticker"`
	Title      string          `json:"title"`
	Features   []string        `json:"features"`
	PriceCents int             `json:"price_cents"`
	SentAt     time.Time       `json:"sent_at"`
	Label      SuggestionLabel `json:"label"`
}

// AlertedMarket tracks dedup info per market ticker.
type AlertedMarket struct {
	Ticker         string    `json:"ticker"`
	LastPriceCents int       `json:"last_price_cents"`
	LastAlertedAt  time.Time `json:"last_alerted_at"`
	Suppressed     bool      `json:"suppressed"` // PASSed markets never re-alert
	SuggestionID   int       `json:"suggestion_id"`
}

// FeatureStats holds accept/reject counts for one learned feature.
type FeatureStats struct {
	Accepts int `json:"accepts"`
	Rejects int `json:"rejects"`
}

// Data is the full persisted state for one user (phone number).
type Data struct {
	Onboarded        bool                      `json:"onboarded"`
	Paused           bool                      `json:"paused"`
	Categories       []string                  `json:"categories"`
	CategoryMenu     []string                  `json:"category_menu"`
	NextSuggestionID int                       `json:"next_suggestion_id"`
	Suggestions      map[string]*Suggestion    `json:"suggestions"`
	Alerted          map[string]*AlertedMarket `json:"alerted"`
	Features         map[string]*FeatureStats  `json:"features"`
	KnownPositions   map[string]bool           `json:"known_positions"`
}

// Store is a concurrency-safe libSQL-backed store scoped by phone number.
type Store struct {
	mu     sync.Mutex
	db     *sql.DB
	logger *slog.Logger
}

// Open wraps an existing libSQL connection.
func Open(db *sql.DB, logger *slog.Logger) *Store {
	return &Store{
		db:     db,
		logger: logger.With("component", "state"),
	}
}

// EnsureUser creates a user row for phone if it does not exist yet.
func (s *Store) EnsureUser(ctx context.Context, phone string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (phone, created_at, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(phone) DO NOTHING`,
		phone, now, now)
	if err != nil {
		return fmt.Errorf("ensuring user %s: %w", phone, err)
	}
	return nil
}

// Update runs fn with exclusive access to the user's state and persists afterwards.
func (s *Store) Update(ctx context.Context, phone string, fn func(*Data)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureUserLocked(ctx, phone); err != nil {
		return err
	}
	data, err := s.loadLocked(ctx, phone)
	if err != nil {
		return err
	}
	fn(data)
	return s.saveLocked(ctx, phone, data)
}

// View runs fn with read access to the user's state.
func (s *Store) View(ctx context.Context, phone string, fn func(*Data)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureUserLocked(ctx, phone); err != nil {
		return err
	}
	data, err := s.loadLocked(ctx, phone)
	if err != nil {
		return err
	}
	fn(data)
	return nil
}

func (s *Store) ensureUserLocked(ctx context.Context, phone string) error {
	return s.EnsureUser(ctx, phone)
}

func (s *Store) loadLocked(ctx context.Context, phone string) (*Data, error) {
	data := &Data{
		NextSuggestionID: 1,
		Suggestions:      map[string]*Suggestion{},
		Alerted:          map[string]*AlertedMarket{},
		Features:         map[string]*FeatureStats{},
		KnownPositions:   map[string]bool{},
	}

	var menuJSON, catsJSON string
	var onboarded, paused int
	err := s.db.QueryRowContext(ctx, `
		SELECT onboarded, paused, category_menu, categories, next_suggestion_id
		FROM users WHERE phone = ?`, phone).Scan(
		&onboarded, &paused, &menuJSON, &catsJSON, &data.NextSuggestionID)
	if err != nil {
		return nil, fmt.Errorf("loading user %s: %w", phone, err)
	}
	data.Onboarded = onboarded != 0
	data.Paused = paused != 0
	if err := json.Unmarshal([]byte(menuJSON), &data.CategoryMenu); err != nil {
		return nil, fmt.Errorf("decoding category_menu for %s: %w", phone, err)
	}
	if err := json.Unmarshal([]byte(catsJSON), &data.Categories); err != nil {
		return nil, fmt.Errorf("decoding categories for %s: %w", phone, err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ticker, title, features, price_cents, sent_at, label
		FROM suggestions WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading suggestions for %s: %w", phone, err)
	}
	defer rows.Close()
	for rows.Next() {
		var sug Suggestion
		var featuresJSON, sentAt, label string
		if err := rows.Scan(&sug.ID, &sug.Ticker, &sug.Title, &featuresJSON,
			&sug.PriceCents, &sentAt, &label); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(featuresJSON), &sug.Features); err != nil {
			return nil, fmt.Errorf("decoding suggestion features: %w", err)
		}
		sug.SentAt, err = time.Parse(time.RFC3339Nano, sentAt)
		if err != nil {
			return nil, fmt.Errorf("decoding suggestion sent_at: %w", err)
		}
		sug.Label = SuggestionLabel(label)
		data.Suggestions[strconv.Itoa(sug.ID)] = &sug
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT ticker, last_price_cents, last_alerted_at, suppressed, suggestion_id
		FROM alerted_markets WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading alerted markets for %s: %w", phone, err)
	}
	defer rows.Close()
	for rows.Next() {
		var am AlertedMarket
		var alertedAt string
		var suppressed int
		if err := rows.Scan(&am.Ticker, &am.LastPriceCents, &alertedAt, &suppressed, &am.SuggestionID); err != nil {
			return nil, err
		}
		am.LastAlertedAt, err = time.Parse(time.RFC3339Nano, alertedAt)
		if err != nil {
			return nil, fmt.Errorf("decoding alerted_at: %w", err)
		}
		am.Suppressed = suppressed != 0
		data.Alerted[am.Ticker] = &am
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT feature, accepts, rejects FROM feature_stats WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading feature stats for %s: %w", phone, err)
	}
	defer rows.Close()
	for rows.Next() {
		var feature string
		var st FeatureStats
		if err := rows.Scan(&feature, &st.Accepts, &st.Rejects); err != nil {
			return nil, err
		}
		data.Features[feature] = &st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT ticker FROM known_positions WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading known positions for %s: %w", phone, err)
	}
	defer rows.Close()
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, err
		}
		data.KnownPositions[ticker] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.logger.Debug("user state loaded", "phone", phone,
		"onboarded", data.Onboarded, "categories", len(data.Categories),
		"suggestions", len(data.Suggestions), "alerted_markets", len(data.Alerted))
	return data, nil
}

func (s *Store) saveLocked(ctx context.Context, phone string, d *Data) error {
	menuJSON, err := json.Marshal(d.CategoryMenu)
	if err != nil {
		return err
	}
	catsJSON, err := json.Marshal(d.Categories)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		UPDATE users SET
			onboarded = ?, paused = ?, category_menu = ?, categories = ?,
			next_suggestion_id = ?, updated_at = ?
		WHERE phone = ?`,
		boolInt(d.Onboarded), boolInt(d.Paused), string(menuJSON), string(catsJSON),
		d.NextSuggestionID, now, phone)
	if err != nil {
		return fmt.Errorf("updating user %s: %w", phone, err)
	}

	for _, table := range []string{"suggestions", "alerted_markets", "feature_stats", "known_positions"} {
		_, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE phone = ?", phone)
		if err != nil {
			return fmt.Errorf("clearing %s for %s: %w", table, phone, err)
		}
	}

	for _, sug := range d.Suggestions {
		featuresJSON, err := json.Marshal(sug.Features)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO suggestions (phone, id, ticker, title, features, price_cents, sent_at, label)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			phone, sug.ID, sug.Ticker, sug.Title, string(featuresJSON),
			sug.PriceCents, sug.SentAt.UTC().Format(time.RFC3339Nano), string(sug.Label))
		if err != nil {
			return fmt.Errorf("inserting suggestion %d: %w", sug.ID, err)
		}
	}

	for _, am := range d.Alerted {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO alerted_markets (phone, ticker, last_price_cents, last_alerted_at, suppressed, suggestion_id)
			VALUES (?, ?, ?, ?, ?, ?)`,
			phone, am.Ticker, am.LastPriceCents, am.LastAlertedAt.UTC().Format(time.RFC3339Nano),
			boolInt(am.Suppressed), am.SuggestionID)
		if err != nil {
			return fmt.Errorf("inserting alerted market %s: %w", am.Ticker, err)
		}
	}

	for feature, st := range d.Features {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO feature_stats (phone, feature, accepts, rejects)
			VALUES (?, ?, ?, ?)`,
			phone, feature, st.Accepts, st.Rejects)
		if err != nil {
			return fmt.Errorf("inserting feature stat %s: %w", feature, err)
		}
	}

	for ticker := range d.KnownPositions {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO known_positions (phone, ticker) VALUES (?, ?)`,
			phone, ticker)
		if err != nil {
			return fmt.Errorf("inserting known position %s: %w", ticker, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing user state for %s: %w", phone, err)
	}
	s.logger.Debug("user state saved", "phone", phone)
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
