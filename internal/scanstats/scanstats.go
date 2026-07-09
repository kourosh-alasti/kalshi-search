// Package scanstats tracks the latest scanner cycle for health reporting.
package scanstats

import (
	"sync"
	"time"
)

// Snapshot is a point-in-time view of scanner health.
type Snapshot struct {
	Healthy          bool      `json:"healthy"`
	LastCycleAt      time.Time `json:"last_cycle_at"`
	LastCycleMS      int64     `json:"last_cycle_duration_ms"`
	EventsFetched    int       `json:"events_fetched"`
	UsersScanned     int       `json:"users_scanned"`
	AlertsSent       int       `json:"alerts_sent"`
	LastError        string    `json:"last_error,omitempty"`
	InboundPending   int       `json:"inbound_pending"`
	IsLeader         bool      `json:"is_leader"`
	CycleTimedOut    bool      `json:"cycle_timed_out"`
}

// Tracker holds the latest scan cycle metrics (thread-safe).
type Tracker struct {
	mu sync.RWMutex
	Snapshot
}

// Update replaces the snapshot.
func (t *Tracker) Update(s Snapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Snapshot = s
}

// Get returns a copy of the current snapshot.
func (t *Tracker) Get() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Snapshot
}

// SetHealthy updates only the healthy flag.
func (t *Tracker) SetHealthy(ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Healthy = ok
}

// SetLeader updates leadership status.
func (t *Tracker) SetLeader(leader bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.IsLeader = leader
}

// SetInboundPending updates the inbound queue depth.
func (t *Tracker) SetInboundPending(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.InboundPending = n
}
