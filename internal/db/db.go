// Package db opens a libSQL connection and applies the schema migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	phone TEXT PRIMARY KEY,
	onboarded INTEGER NOT NULL DEFAULT 0,
	paused INTEGER NOT NULL DEFAULT 0,
	category_menu TEXT NOT NULL DEFAULT '[]',
	categories TEXT NOT NULL DEFAULT '[]',
	next_suggestion_id INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS suggestions (
	phone TEXT NOT NULL,
	id INTEGER NOT NULL,
	ticker TEXT NOT NULL,
	title TEXT NOT NULL,
	features TEXT NOT NULL,
	price_cents INTEGER NOT NULL,
	sent_at TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT 'pending',
	PRIMARY KEY (phone, id)
);

CREATE TABLE IF NOT EXISTS alerted_markets (
	phone TEXT NOT NULL,
	ticker TEXT NOT NULL,
	last_price_cents INTEGER NOT NULL,
	last_alerted_at TEXT NOT NULL,
	suppressed INTEGER NOT NULL DEFAULT 0,
	suggestion_id INTEGER NOT NULL,
	PRIMARY KEY (phone, ticker)
);

CREATE TABLE IF NOT EXISTS feature_stats (
	phone TEXT NOT NULL,
	feature TEXT NOT NULL,
	accepts INTEGER NOT NULL DEFAULT 0,
	rejects INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (phone, feature)
);

CREATE TABLE IF NOT EXISTS known_positions (
	phone TEXT NOT NULL,
	ticker TEXT NOT NULL,
	PRIMARY KEY (phone, ticker)
);

CREATE TABLE IF NOT EXISTS suggestion_records (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	phone TEXT NOT NULL,
	kind TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	suggestion_id INTEGER NOT NULL,
	ticker TEXT,
	title TEXT,
	category TEXT,
	series_ticker TEXT,
	yes_ask INTEGER,
	yes_bid INTEGER,
	volume INTEGER,
	open_interest INTEGER,
	close_time TEXT,
	features TEXT,
	score REAL,
	label TEXT
);

CREATE INDEX IF NOT EXISTS idx_suggestions_phone_label ON suggestions(phone, label);
CREATE INDEX IF NOT EXISTS idx_suggestion_records_phone ON suggestion_records(phone);

CREATE TABLE IF NOT EXISTS onboarding_tokens (
	token TEXT PRIMARY KEY,
	phone TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	used INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_onboarding_tokens_phone ON onboarding_tokens(phone);

CREATE TABLE IF NOT EXISTS inbound_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	phone TEXT NOT NULL,
	body TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	received_at TEXT NOT NULL,
	processed_at TEXT,
	error TEXT
);
CREATE INDEX IF NOT EXISTS idx_inbound_pending ON inbound_messages(status, received_at);

CREATE TABLE IF NOT EXISTS watchlist (
	phone TEXT NOT NULL,
	ticker TEXT NOT NULL,
	max_price_cents INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	PRIMARY KEY (phone, ticker)
);

CREATE TABLE IF NOT EXISTS scanner_locks (
	lock_name TEXT PRIMARY KEY,
	holder_id TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`

var migrations = []string{
	`ALTER TABLE users ADD COLUMN tags_by_category TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE users ADD COLUMN subcategories TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE users ADD COLUMN filter_overrides TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE users ADD COLUMN quiet_hours TEXT NOT NULL DEFAULT '{"enabled":false,"start_hour":22,"end_hour":8,"timezone":"America/New_York"}'`,
	`ALTER TABLE alerted_markets ADD COLUMN suppressed_until TEXT`,
	`ALTER TABLE alerted_markets ADD COLUMN last_volume INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE alerted_markets ADD COLUMN last_close_hours REAL NOT NULL DEFAULT 0`,
}

// Open connects to libSQL and runs schema migrations.
func Open(ctx context.Context, url, authToken string) (*sql.DB, error) {
	dsn := buildDSN(url, authToken)
	db, err := sql.Open("libsql", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening libsql: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging libsql: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	for _, stmt := range migrations {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				db.Close()
				return nil, fmt.Errorf("applying migration %q: %w", stmt, err)
			}
		}
	}
	return db, nil
}

func buildDSN(url, authToken string) string {
	if authToken == "" {
		return url
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return url + sep + "authToken=" + authToken
}
