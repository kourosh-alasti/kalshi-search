package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kourosh/kalshi-search/internal/scanstats"
)

func TestHandleHealthScannerDisabled(t *testing.T) {
	stats := &scanstats.Tracker{}
	s := &Server{stats: stats, scannerEnabled: false}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var snap scanstats.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Healthy {
		t.Fatal("expected healthy=true when scanner disabled")
	}
}

func TestHandleHealthScannerEnabledUnhealthy(t *testing.T) {
	stats := &scanstats.Tracker{}
	s := &Server{stats: stats, scannerEnabled: true}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
