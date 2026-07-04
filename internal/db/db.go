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
`

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
