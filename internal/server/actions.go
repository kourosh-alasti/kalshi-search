package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/kourosh/kalshi-search/internal/scanstats"
	"github.com/kourosh/kalshi-search/internal/state"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !s.scannerEnabled {
		snap := scanstats.Snapshot{Healthy: true}
		if s.stats != nil {
			snap = s.stats.Get()
			snap.Healthy = true
		}
		writeHealth(w, http.StatusOK, snap)
		return
	}
	if s.stats != nil {
		snap := s.stats.Get()
		if !snap.Healthy {
			writeHealth(w, http.StatusServiceUnavailable, snap)
			return
		}
		writeHealth(w, http.StatusOK, snap)
		return
	}
	if !s.healthy.Load() {
		http.Error(w, "last scan failed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func writeHealth(w http.ResponseWriter, code int, snap scanstats.Snapshot) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(snap)
}

func writeHTMLMessage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><body><h2>Kalshi Alerts</h2><p>%s</p></body></html>`, html.EscapeString(msg))
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	userEnc := r.PathValue("user")
	idStr := r.PathValue("id")
	suggestionID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	action := r.URL.Query().Get("action")
	exp, _ := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	sig := r.URL.Query().Get("sig")

	phone, err := s.signer.VerifyFeedback(userEnc, suggestionID, action, exp, sig)
	if err != nil {
		http.Error(w, "invalid or expired link", http.StatusForbidden)
		return
	}
	if !s.allowedRecipients[phone] {
		http.Error(w, "unknown user", http.StatusForbidden)
		return
	}

	took := strings.EqualFold(action, "took")
	msg, _ := s.handler.ApplyFeedback(r.Context(), phone, took, suggestionID)
	writeHTMLMessage(w, msg)
}

func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	userEnc := r.PathValue("user")
	action := r.URL.Query().Get("action")
	exp, _ := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	sig := r.URL.Query().Get("sig")

	phone, err := s.signer.VerifyToggle(userEnc, action, exp, sig)
	if err != nil {
		http.Error(w, "invalid or expired link", http.StatusForbidden)
		return
	}
	if !s.allowedRecipients[phone] {
		http.Error(w, "unknown user", http.StatusForbidden)
		return
	}

	enabled := strings.EqualFold(action, "enable")
	var msg string
	if err := s.store.Update(r.Context(), phone, func(d *state.Data) {
		d.QuietHours.Enabled = enabled
	}); err != nil {
		msg = "Something went wrong saving your preference. Please try again."
	} else if enabled {
		msg = "Quiet hours enabled."
	} else {
		msg = "Quiet hours disabled."
	}
	writeHTMLMessage(w, msg)
}
