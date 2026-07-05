// Package server exposes the HTTP endpoints: SMS webhook receivers and a health
// check for Railway.
package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/kourosh/kalshi-search/internal/commands"
	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/state"
	"github.com/kourosh/kalshi-search/internal/telnyx"
	"github.com/kourosh/kalshi-search/internal/twilio"
)

// Server holds HTTP handler state.
type Server struct {
	notifyChannel    string
	telnyxPublicKey  string
	twilioAuthToken  string
	twilioWebhookURL string
	allowedRecipients map[string]bool
	handler          *commands.Handler
	store            *state.Store
	logger           *slog.Logger
	healthy          atomic.Bool
}

// New creates the server. SMS commands are only accepted from alert recipients
// when NOTIFY_CHANNEL=sms. Health starts true so Railway's initial check
// passes before the first scan completes.
func New(cfg *config.Config, handler *commands.Handler, store *state.Store, logger *slog.Logger) *Server {
	allowed := make(map[string]bool, len(cfg.AlertRecipients))
	for _, r := range cfg.AlertRecipients {
		allowed[r] = true
	}
	s := &Server{
		notifyChannel:     cfg.NotifyChannel,
		telnyxPublicKey:   cfg.TelnyxPublicKey,
		twilioAuthToken:   cfg.TwilioAuthToken,
		twilioWebhookURL:  cfg.TwilioWebhookURL,
		allowedRecipients: allowed,
		handler:           handler,
		store:             store,
		logger:            logger.With("component", "server"),
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
	if s.notifyChannel == "sms" {
		mux.HandleFunc("POST /webhooks/telnyx", s.handleTelnyxWebhook)
		mux.HandleFunc("POST /webhooks/twilio", s.handleTwilioWebhook)
	}
	mux.HandleFunc("GET /opt-in", staticHTML(optInPage))
	mux.HandleFunc("GET /onboard/{token}", s.handleOnboardGet)
	mux.HandleFunc("POST /onboard/{token}", s.handleOnboardPost)
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

func staticHTML(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
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

func (s *Server) handleTelnyxWebhook(w http.ResponseWriter, r *http.Request) {
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
	if !s.allowedRecipients[msg.From] {
		s.logger.Warn("ignoring inbound message from unknown recipient", "from", msg.From)
		return
	}

	// The request context dies when this handler returns, so use a fresh one.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.handler.Handle(ctx, msg.From, msg.Text)
	}()
}

func (s *Server) handleTwilioWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	params, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	if s.twilioAuthToken != "" && s.twilioWebhookURL != "" {
		err := twilio.VerifySignature(
			s.twilioAuthToken,
			s.twilioWebhookURL,
			r.Header.Get("X-Twilio-Signature"),
			params,
		)
		if err != nil {
			s.logger.Warn("twilio webhook rejected", "error", err, "remote", r.RemoteAddr)
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
	} else {
		s.logger.Warn("TWILIO_AUTH_TOKEN or TWILIO_WEBHOOK_URL not set; accepting webhook without verification")
	}

	msg, err := twilio.ParseInbound(body)
	if err != nil {
		s.logger.Error("twilio webhook parse failed", "error", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Acknowledge quickly; process the command asynchronously.
	w.WriteHeader(http.StatusOK)

	if msg == nil {
		s.logger.Debug("ignoring twilio webhook without inbound message fields")
		return
	}
	if !s.allowedRecipients[msg.From] {
		s.logger.Warn("ignoring inbound message from unknown recipient", "from", msg.From)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.handler.Handle(ctx, msg.From, msg.Text)
	}()
}
