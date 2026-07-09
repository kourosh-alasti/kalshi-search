// Package state persists per-user mutable service state (category subscriptions,
// alert dedup, suggestion feedback, learned feature stats) in libSQL.
package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sort"
	"sync"
	"time"
)

// SuggestionLabel is the feedback status of a suggestion.
type SuggestionLabel string

const (
	LabelPending  SuggestionLabel = "pending"
	LabelTook     SuggestionLabel = "took"
	LabelPass     SuggestionLabel = "pass"
	LabelPosition SuggestionLabel = "position"
)

// FilterOverrides holds per-user scanner filter overrides (nil = use global default).
type FilterOverrides struct {
	MaxPriceCents    *int `json:"max_price_cents,omitempty"`
	MinVolume        *int `json:"min_volume,omitempty"`
	MinOpenInterest  *int `json:"min_open_interest,omitempty"`
	CloseWithinHours *int `json:"close_within_hours,omitempty"`
}

// QuietHours defines when alerts are held unless the user disables the feature.
type QuietHours struct {
	Enabled   bool   `json:"enabled"`
	StartHour int    `json:"start_hour"`
	EndHour   int    `json:"end_hour"`
	Timezone  string `json:"timezone"`
}

// DefaultQuietHours returns sensible defaults (10pm–8am US Eastern).
func DefaultQuietHours() QuietHours {
	return QuietHours{Enabled: false, StartHour: 22, EndHour: 8, Timezone: "America/New_York"}
}

// WatchItem is a user-requested ticker alert.
type WatchItem struct {
	Ticker         string    `json:"ticker"`
	MaxPriceCents  int       `json:"max_price_cents"`
	CreatedAt      time.Time `json:"created_at"`
}

// Suggestion is one alert we texted, awaiting or holding feedback.
type Suggestion struct {
	ID         int             `json:"id"`
	Ticker     string          `json:"ticker"`
	Title      string          `json:"title"`
	Features   []string        `json:"features"`
	PriceCents int             `json:"price_cents"`
	SentAt     time.Time       `json:"sent_at"`
	Label      SuggestionLabel `json:"label"`
	Score      float64         `json:"score"`
	Explain    string          `json:"explain,omitempty"`
}

// AlertedMarket tracks dedup info per market ticker.
type AlertedMarket struct {
	Ticker          string    `json:"ticker"`
	LastPriceCents  int       `json:"last_price_cents"`
	LastAlertedAt   time.Time `json:"last_alerted_at"`
	Suppressed      bool      `json:"suppressed"`
	SuppressedUntil time.Time `json:"suppressed_until,omitempty"`
	SuggestionID    int       `json:"suggestion_id"`
	LastVolume      int       `json:"last_volume"`
	LastCloseHours  float64   `json:"last_close_hours"`
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
	Subcategories    map[string][]string       `json:"subcategories"`
	CategoryMenu     []string                  `json:"category_menu"`
	TagsByCategory   map[string][]string       `json:"tags_by_category"`
	FilterOverrides  FilterOverrides           `json:"filter_overrides"`
	QuietHours       QuietHours                `json:"quiet_hours"`
	Watchlist        map[string]*WatchItem     `json:"watchlist"`
	NextSuggestionID int                       `json:"next_suggestion_id"`
	Suggestions      map[string]*Suggestion    `json:"suggestions"`
	Alerted          map[string]*AlertedMarket `json:"alerted"`
	Features         map[string]*FeatureStats  `json:"features"`
	KnownPositions   map[string]bool           `json:"known_positions"`
}

// Store is a concurrency-safe libSQL-backed store scoped by phone number.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
	locks  sync.Map // phone -> *sync.Mutex
}

// Open wraps an existing libSQL connection.
func Open(db *sql.DB, logger *slog.Logger) *Store {
	return &Store{
		db:     db,
		logger: logger.With("component", "state"),
	}
}

func (s *Store) userLock(phone string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(phone, &sync.Mutex{})
	return v.(*sync.Mutex)
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
	mu := s.userLock(phone)
	mu.Lock()
	defer mu.Unlock()

	if err := s.ensureUserLocked(ctx, phone); err != nil {
		return err
	}
	data, err := s.loadLocked(ctx, phone)
	if err != nil {
		return err
	}
	fn(data)
	pruneSuggestions(data, 200)
	return s.saveLocked(ctx, phone, data)
}

// View runs fn with read access to the user's state.
func (s *Store) View(ctx context.Context, phone string, fn func(*Data)) error {
	mu := s.userLock(phone)
	mu.Lock()
	defer mu.Unlock()

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
		Watchlist:        map[string]*WatchItem{},
		QuietHours:       DefaultQuietHours(),
	}

	var menuJSON, catsJSON, tagsJSON, subsJSON, filtersJSON, quietJSON string
	var onboarded, paused int
	err := s.db.QueryRowContext(ctx, `
		SELECT onboarded, paused, category_menu, categories, tags_by_category, subcategories,
			filter_overrides, quiet_hours, next_suggestion_id
		FROM users WHERE phone = ?`, phone).Scan(
		&onboarded, &paused, &menuJSON, &catsJSON, &tagsJSON, &subsJSON,
		&filtersJSON, &quietJSON, &data.NextSuggestionID)
	if err != nil {
		return nil, fmt.Errorf("loading user %s: %w", phone, err)
	}
	data.Onboarded = onboarded != 0
	data.Paused = paused != 0
	if err := decodeJSON(menuJSON, &data.CategoryMenu); err != nil {
		return nil, fmt.Errorf("decoding category_menu for %s: %w", phone, err)
	}
	if err := decodeJSON(catsJSON, &data.Categories); err != nil {
		return nil, fmt.Errorf("decoding categories for %s: %w", phone, err)
	}
	if tagsJSON == "" {
		tagsJSON = "{}"
	}
	if err := decodeJSON(tagsJSON, &data.TagsByCategory); err != nil {
		return nil, fmt.Errorf("decoding tags_by_category for %s: %w", phone, err)
	}
	if subsJSON == "" {
		subsJSON = "{}"
	}
	if err := decodeJSON(subsJSON, &data.Subcategories); err != nil {
		return nil, fmt.Errorf("decoding subcategories for %s: %w", phone, err)
	}
	if filtersJSON == "" {
		filtersJSON = "{}"
	}
	if err := decodeJSON(filtersJSON, &data.FilterOverrides); err != nil {
		return nil, fmt.Errorf("decoding filter_overrides for %s: %w", phone, err)
	}
	if quietJSON == "" {
		quietJSON = `{"enabled":false,"start_hour":22,"end_hour":8,"timezone":"America/New_York"}`
	}
	if err := decodeJSON(quietJSON, &data.QuietHours); err != nil {
		return nil, fmt.Errorf("decoding quiet_hours for %s: %w", phone, err)
	}
	if data.TagsByCategory == nil {
		data.TagsByCategory = map[string][]string{}
	}
	if data.Subcategories == nil {
		data.Subcategories = map[string][]string{}
	}
	if data.QuietHours.Timezone == "" {
		data.QuietHours.Timezone = "America/New_York"
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ticker, title, features, price_cents, sent_at, label
		FROM suggestions WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading suggestions for %s: %w", phone, err)
	}
	for rows.Next() {
		var sug Suggestion
		var featuresJSON, sentAt, label string
		if err := rows.Scan(&sug.ID, &sug.Ticker, &sug.Title, &featuresJSON,
			&sug.PriceCents, &sentAt, &label); err != nil {
			rows.Close()
			return nil, err
		}
		if err := decodeJSON(featuresJSON, &sug.Features); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decoding suggestion features: %w", err)
		}
		sug.SentAt, err = time.Parse(time.RFC3339Nano, sentAt)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("decoding suggestion sent_at: %w", err)
		}
		sug.Label = SuggestionLabel(label)
		data.Suggestions[strconv.Itoa(sug.ID)] = &sug
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
		SELECT ticker, last_price_cents, last_alerted_at, suppressed, suggestion_id,
			suppressed_until, last_volume, last_close_hours
		FROM alerted_markets WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading alerted markets for %s: %w", phone, err)
	}
	for rows.Next() {
		var am AlertedMarket
		var alertedAt string
		var suppressed int
		var suppressedUntil sql.NullString
		if err := rows.Scan(&am.Ticker, &am.LastPriceCents, &alertedAt, &suppressed, &am.SuggestionID,
			&suppressedUntil, &am.LastVolume, &am.LastCloseHours); err != nil {
			rows.Close()
			return nil, err
		}
		am.LastAlertedAt, err = time.Parse(time.RFC3339Nano, alertedAt)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("decoding alerted_at: %w", err)
		}
		am.Suppressed = suppressed != 0
		if suppressedUntil.Valid && suppressedUntil.String != "" {
			am.SuppressedUntil, _ = time.Parse(time.RFC3339Nano, suppressedUntil.String)
		}
		data.Alerted[am.Ticker] = &am
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
		SELECT feature, accepts, rejects FROM feature_stats WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading feature stats for %s: %w", phone, err)
	}
	for rows.Next() {
		var feature string
		var st FeatureStats
		if err := rows.Scan(&feature, &st.Accepts, &st.Rejects); err != nil {
			rows.Close()
			return nil, err
		}
		data.Features[feature] = &st
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
		SELECT ticker FROM known_positions WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading known positions for %s: %w", phone, err)
	}
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			rows.Close()
			return nil, err
		}
		data.KnownPositions[ticker] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
		SELECT ticker, max_price_cents, created_at FROM watchlist WHERE phone = ?`, phone)
	if err != nil {
		return nil, fmt.Errorf("loading watchlist for %s: %w", phone, err)
	}
	for rows.Next() {
		var item WatchItem
		var created string
		if err := rows.Scan(&item.Ticker, &item.MaxPriceCents, &created); err != nil {
			rows.Close()
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		data.Watchlist[item.Ticker] = &item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	s.logger.Debug("user state loaded", "phone", phone,
		"onboarded", data.Onboarded, "categories", len(data.Categories),
		"suggestions", len(data.Suggestions), "alerted_markets", len(data.Alerted),
		"watchlist", len(data.Watchlist))
	return data, nil
}

func decodeJSON(src string, dest any) error {
	if src == "" || src == "null" {
		return nil
	}
	return json.Unmarshal([]byte(src), dest)
}

func (s *Store) saveLocked(ctx context.Context, phone string, d *Data) error {
	menuJSON, _ := json.Marshal(d.CategoryMenu)
	catsJSON, _ := json.Marshal(d.Categories)
	tagsJSON, _ := json.Marshal(d.TagsByCategory)
	subsJSON, _ := json.Marshal(d.Subcategories)
	filtersJSON, _ := json.Marshal(d.FilterOverrides)
	quietJSON, _ := json.Marshal(d.QuietHours)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		UPDATE users SET
			onboarded = ?, paused = ?, category_menu = ?, categories = ?,
			tags_by_category = ?, subcategories = ?,
			filter_overrides = ?, quiet_hours = ?,
			next_suggestion_id = ?, updated_at = ?
		WHERE phone = ?`,
		boolInt(d.Onboarded), boolInt(d.Paused), string(menuJSON), string(catsJSON),
		string(tagsJSON), string(subsJSON), string(filtersJSON), string(quietJSON),
		d.NextSuggestionID, now, phone)
	if err != nil {
		return fmt.Errorf("updating user %s: %w", phone, err)
	}

	for _, table := range []string{"suggestions", "alerted_markets", "feature_stats", "known_positions", "watchlist"} {
		_, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE phone = ?", phone)
		if err != nil {
			return fmt.Errorf("clearing %s for %s: %w", table, phone, err)
		}
	}

	for _, sug := range d.Suggestions {
		featuresJSON, _ := json.Marshal(sug.Features)
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
		var suppressedUntil sql.NullString
		if !am.SuppressedUntil.IsZero() {
			suppressedUntil = sql.NullString{String: am.SuppressedUntil.UTC().Format(time.RFC3339Nano), Valid: true}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO alerted_markets (phone, ticker, last_price_cents, last_alerted_at, suppressed,
				suggestion_id, suppressed_until, last_volume, last_close_hours)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			phone, am.Ticker, am.LastPriceCents, am.LastAlertedAt.UTC().Format(time.RFC3339Nano),
			boolInt(am.Suppressed), am.SuggestionID, suppressedUntil, am.LastVolume, am.LastCloseHours)
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

	for _, item := range d.Watchlist {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO watchlist (phone, ticker, max_price_cents, created_at)
			VALUES (?, ?, ?, ?)`,
			phone, item.Ticker, item.MaxPriceCents, item.CreatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("inserting watchlist %s: %w", item.Ticker, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing user state for %s: %w", phone, err)
	}
	s.logger.Debug("user state saved", "phone", phone)
	return nil
}

func pruneSuggestions(d *Data, maxKeep int) {
	if len(d.Suggestions) <= maxKeep {
		return
	}
	type entry struct {
		id  string
		sug *Suggestion
	}
	var labeled []entry
	for id, sug := range d.Suggestions {
		if sug.Label == LabelPending {
			continue
		}
		labeled = append(labeled, entry{id, sug})
	}
	sort.Slice(labeled, func(i, j int) bool {
		return labeled[i].sug.SentAt.Before(labeled[j].sug.SentAt)
	})
	for _, e := range labeled {
		if len(d.Suggestions) <= maxKeep {
			break
		}
		delete(d.Suggestions, e.id)
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// OnboardingToken is a short-lived link for category selection.
type OnboardingToken struct {
	Token     string
	Phone     string
	ExpiresAt time.Time
	Used      bool
}

// CreateOnboardingToken generates and stores a single-use onboarding link token.
func (s *Store) CreateOnboardingToken(ctx context.Context, phone string, ttl time.Duration) (string, error) {
	if err := s.EnsureUser(ctx, phone); err != nil {
		return "", err
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(raw)
	now := time.Now().UTC()
	expires := now.Add(ttl)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO onboarding_tokens (token, phone, expires_at, used, created_at)
		VALUES (?, ?, ?, 0, ?)`,
		token, phone, expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return "", fmt.Errorf("saving onboarding token: %w", err)
	}
	return token, nil
}

// LookupOnboardingToken returns token metadata when the link is still valid.
func (s *Store) LookupOnboardingToken(ctx context.Context, token string) (*OnboardingToken, error) {
	var phone, expiresAt string
	var used int
	err := s.db.QueryRowContext(ctx, `
		SELECT phone, expires_at, used FROM onboarding_tokens WHERE token = ?`, token).
		Scan(&phone, &expiresAt, &used)
	if err != nil {
		return nil, fmt.Errorf("token not found")
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("decoding token expiry: %w", err)
	}
	t := &OnboardingToken{Token: token, Phone: phone, ExpiresAt: expires, Used: used != 0}
	if t.Used {
		return nil, fmt.Errorf("token already used")
	}
	if time.Now().After(t.ExpiresAt) {
		return nil, fmt.Errorf("token expired")
	}
	return t, nil
}

// MarkOnboardingTokenUsed marks a token consumed after preferences are saved.
func (s *Store) MarkOnboardingTokenUsed(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE onboarding_tokens SET used = 1 WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("marking token used: %w", err)
	}
	return nil
}

// InQuietHours reports whether now falls in the user's configured quiet window.
func InQuietHours(q QuietHours, now time.Time) bool {
	if !q.Enabled {
		return false
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	hour := local.Hour()
	start, end := q.StartHour, q.EndHour
	if start == end {
		return false
	}
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}
