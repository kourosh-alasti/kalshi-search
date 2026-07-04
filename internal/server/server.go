// Package server exposes the HTTP endpoints: the Telnyx webhook receiver
// and a health check for Railway.
package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kourosh/kalshi-search/internal/commands"
	"github.com/kourosh/kalshi-search/internal/telnyx"
)

// Server holds HTTP handler state.
type Server struct {
	telnyxPublicKey string
	allowedPhones   map[string]bool
	handler         *commands.Handler
	logger          *slog.Logger
	healthy         atomic.Bool
}

// New creates the server. Commands are only accepted from alertPhones.
// Health starts true so Railway's initial check passes before the first
// scan completes.
func New(telnyxPublicKey string, alertPhones []string, handler *commands.Handler, logger *slog.Logger) *Server {
	allowed := make(map[string]bool, len(alertPhones))
	for _, p := range alertPhones {
		allowed[p] = true
	}
	s := &Server{
		telnyxPublicKey: telnyxPublicKey,
		allowedPhones:   allowed,
		handler:         handler,
		logger:          logger.With("component", "server"),
	}
	s.healthy.Store(true)
	return s
}

// SetHealthy updates the health flag reported by /healthz.
func (s *Server) SetHealthy(ok bool) { s.healthy.Store(ok) }

// Routes returns the HTTP mux.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /webhooks/telnyx", s.handleWebhook)
	mux.HandleFunc("GET /privacy", staticText(privacyPolicy))
	mux.HandleFunc("GET /terms", staticText(termsAndConditions))
	return mux
}

func staticText(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if !s.healthy.Load() {
		http.Error(w, "last scan failed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	if s.telnyxPublicKey != "" {
		err := telnyx.VerifySignature(
			s.telnyxPublicKey,
			r.Header.Get("telnyx-timestamp"),
			r.Header.Get("telnyx-signature-ed25519"),
			body,
			5*time.Minute,
		)
		if err != nil {
			s.logger.Warn("webhook rejected", "error", err, "remote", r.RemoteAddr)
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
	} else {
		s.logger.Warn("TELNYX_PUBLIC_KEY not set; accepting webhook without verification")
	}

	msg, err := telnyx.ParseInbound(body)
	if err != nil {
		s.logger.Error("webhook parse failed", "error", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Acknowledge quickly (Telnyx requires a 2xx within 2 seconds); process
	// the command asynchronously.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"received":true}`))

	if msg == nil {
		s.logger.Debug("ignoring non-inbound webhook event")
		return
	}
	if !s.allowedPhones[msg.From] {
		s.logger.Warn("ignoring inbound message from unknown number", "from", msg.From)
		return
	}

	// The request context dies when this handler returns, so use a fresh one.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.handler.Handle(ctx, msg.From, msg.Text)
	}()
}
