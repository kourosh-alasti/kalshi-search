// Package inbound persists and processes durable inbound SMS commands.
package inbound

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// Queue stores inbound messages for at-least-once processing.
type Queue struct {
	db     *sql.DB
	logger *slog.Logger
}

// New returns an inbound message queue.
func New(db *sql.DB, logger *slog.Logger) *Queue {
	return &Queue{db: db, logger: logger.With("component", "inbound")}
}

// Enqueue stores a message for later processing.
func (q *Queue) Enqueue(ctx context.Context, from, body string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO inbound_messages (phone, body, status, received_at)
		VALUES (?, ?, 'pending', ?)`, from, body, now)
	if err != nil {
		return fmt.Errorf("enqueue inbound: %w", err)
	}
	return nil
}

// Message is one queued inbound SMS.
type Message struct {
	ID     int64
	Phone  string
	Body   string
}

// Pending returns unprocessed messages oldest-first.
func (q *Queue) Pending(ctx context.Context, limit int) ([]Message, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, phone, body FROM inbound_messages
		WHERE status = 'pending'
		ORDER BY received_at ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Phone, &m.Body); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkDone marks a message processed.
func (q *Queue) MarkDone(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.db.ExecContext(ctx, `
		UPDATE inbound_messages SET status = 'done', processed_at = ? WHERE id = ?`, now, id)
	return err
}

// MarkFailed marks a message failed with an error note.
func (q *Queue) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.db.ExecContext(ctx, `
		UPDATE inbound_messages SET status = 'failed', processed_at = ?, error = ? WHERE id = ?`,
		now, errMsg, id)
	return err
}

// PendingCount returns how many messages await processing.
func (q *Queue) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM inbound_messages WHERE status = 'pending'`).Scan(&n)
	return n, err
}
