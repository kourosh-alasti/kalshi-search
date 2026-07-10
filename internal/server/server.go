// Package server exposes HTTP endpoints: webhooks, health, onboarding, and signed action links.
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
	"github.com/kourosh/kalshi-search/internal/inbound"
	"github.com/kourosh/kalshi-search/internal/metrics"
	"github.com/kourosh/kalshi-search/internal/scanstats"
	"github.com/kourosh/kalshi-search/internal/sign"
	"github.com/kourosh/kalshi-search/internal/state"
	"github.com/kourosh/kalshi-search/internal/telnyx"
	"github.com/kourosh/kalshi-search/internal/twilio"
)

// Server holds HTTP handler state.
type Server struct {
	notifyChannel     string
	telnyxPublicKey   string
	twilioAuthToken   string
	twilioWebhookURL  string
	allowedRecipients map[string]bool
	handler           *commands.Handler
	store             *state.Store
	inbound           *inbound.Queue
	signer            *sign.Signer
	stats             *scanstats.Tracker
	logger            *slog.Logger
	healthy           atomic.Bool
}

// New creates the server.
func New(cfg *config.Config, handler *commands.Handler, store *state.Store, inboundQ *inbound.Queue,
	signer *sign.Signer, stats *scanstats.Tracker, logger *slog.Logger) *Server {
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
		inbound:           inboundQ,
		signer:            signer,
		stats:             stats,
		logger:            logger.With("component", "server"),
	}
	s.healthy.Store(true)
	return s
}

// SetHealthy updates the health flag reported by /healthz.
func (s *Server) SetHealthy(ok bool) {
	s.healthy.Store(ok)
	if s.stats != nil {
		s.stats.SetHealthy(ok)
	}
}

// Routes returns the HTTP mux.
func (s *Server) Routes(metricsEnabled bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	if metricsEnabled {
		mux.Handle("GET /metrics", metrics.Handler())
	}
	if s.notifyChannel == "sms" {
		mux.HandleFunc("POST /webhooks/telnyx", s.handleTelnyxWebhook)
		mux.HandleFunc("POST /webhooks/twilio", s.handleTwilioWebhook)
	}
	mux.HandleFunc("GET /feedback/{user}/{id}", s.handleFeedback)
	mux.HandleFunc("GET /toggle/{user}", s.handleToggle)
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

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"received":true}`))

	if msg == nil {
		return
	}
	if !s.allowedRecipients[msg.From] {
		s.logger.Warn("ignoring inbound message from unknown recipient", "from", msg.From)
		return
	}
	s.enqueueInbound(msg.From, msg.Text)
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

	w.WriteHeader(http.StatusOK)

	if msg == nil {
		return
	}
	if !s.allowedRecipients[msg.From] {
		s.logger.Warn("ignoring inbound message from unknown recipient", "from", msg.From)
		return
	}
	s.enqueueInbound(msg.From, msg.Text)
}

func (s *Server) enqueueInbound(from, text string) {
	if s.inbound == nil {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.handler.Handle(ctx, from, text); err != nil {
			s.logger.Error("direct inbound handling failed", "error", err, "from", from)
		}
	}()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.inbound.Enqueue(ctx, from, text); err != nil {
		s.logger.Error("enqueue inbound failed", "error", err, "from", from)
	}
}
