// Package leader provides DB-backed leader election for the scanner worker.
package leader

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

const lockName = "scanner"

// Elector acquires and renews a distributed lock so only one instance scans.
type Elector struct {
	db         *sql.DB
	instanceID string
	ttl        time.Duration
	logger     *slog.Logger
}

// New returns an elector. ttl is how long a lock lease lasts without renewal.
func New(db *sql.DB, instanceID string, ttl time.Duration, logger *slog.Logger) *Elector {
	if ttl < 5*time.Second {
		ttl = 30 * time.Second
	}
	return &Elector{
		db: db, instanceID: instanceID, ttl: ttl,
		logger: logger.With("component", "leader"),
	}
}

// TryAcquire attempts to become leader. Returns true if this instance holds the lock.
func (e *Elector) TryAcquire(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	expires := now.Add(e.ttl).Format(time.RFC3339Nano)

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var holder string
	var expiresAt string
	err = tx.QueryRowContext(ctx, `
		SELECT holder_id, expires_at FROM scanner_locks WHERE lock_name = ?`, lockName).
		Scan(&holder, &expiresAt)

	switch {
	case err == sql.ErrNoRows:
		_, err = tx.ExecContext(ctx, `
			INSERT INTO scanner_locks (lock_name, holder_id, expires_at, updated_at)
			VALUES (?, ?, ?, ?)`, lockName, e.instanceID, expires, now.Format(time.RFC3339Nano))
		if err != nil {
			return false, fmt.Errorf("inserting lock: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		e.logger.Info("acquired scanner leadership", "instance", e.instanceID)
		return true, nil
	case err != nil:
		return false, err
	}

	exp, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
	if parseErr != nil {
		exp = time.Time{}
	}

	if holder == e.instanceID || time.Now().After(exp) {
		_, err = tx.ExecContext(ctx, `
			UPDATE scanner_locks SET holder_id = ?, expires_at = ?, updated_at = ?
			WHERE lock_name = ? AND (holder_id = ? OR expires_at < ?)`,
			e.instanceID, expires, now.Format(time.RFC3339Nano), lockName,
			e.instanceID, now.Format(time.RFC3339Nano))
		if err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		if holder != e.instanceID {
			e.logger.Info("acquired scanner leadership", "instance", e.instanceID, "previous", holder)
		}
		return true, nil
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

// RunLoop renews leadership while ctx is active. onLeadership toggles when leadership changes.
func (e *Elector) RunLoop(ctx context.Context, interval time.Duration, onLeadership func(bool)) {
	if interval <= 0 {
		interval = e.ttl / 3
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var wasLeader bool
	for {
		leader, err := e.TryAcquire(ctx)
		if err != nil {
			e.logger.Warn("leader election failed", "error", err)
			leader = false
		}
		if leader != wasLeader {
			onLeadership(leader)
			wasLeader = leader
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
