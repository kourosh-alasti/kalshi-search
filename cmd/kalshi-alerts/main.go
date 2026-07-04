// kalshi-alerts scans Kalshi markets for low-odds opportunities and texts
// them via Telnyx or Twilio. See README.md for setup and configuration.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kourosh/kalshi-search/internal/commands"
	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/db"
	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/learning"
	"github.com/kourosh/kalshi-search/internal/scanner"
	"github.com/kourosh/kalshi-search/internal/server"
	"github.com/kourosh/kalshi-search/internal/sms"
	"github.com/kourosh/kalshi-search/internal/state"
	"github.com/kourosh/kalshi-search/internal/telnyx"
	"github.com/kourosh/kalshi-search/internal/twilio"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	logger.Info("starting kalshi-alerts",
		"sms_provider", cfg.SMSProvider,
		"poll_interval", cfg.PollInterval.String(),
		"max_price_cents", cfg.MaxPriceCents,
		"min_volume", cfg.MinVolume,
		"min_open_interest", cfg.MinOpenInterest,
		"close_within_hours", cfg.CloseWithinHours,
		"libsql_url", cfg.LibSQLURL,
		"log_level", cfg.LogLevel.String())

	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.LibSQLURL, cfg.LibSQLAuthToken)
	if err != nil {
		logger.Error("opening libsql failed", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	store := state.Open(sqlDB, logger)
	for _, phone := range cfg.AlertPhoneNumbers {
		if err := store.EnsureUser(ctx, phone); err != nil {
			logger.Error("creating user failed", "phone", phone, "error", err)
			os.Exit(1)
		}
	}

	kalshiClient, err := kalshi.NewClient(cfg.KalshiBaseURL, cfg.KalshiAPIKeyID, cfg.KalshiPrivateKeyPEM, logger)
	if err != nil {
		logger.Error("creating kalshi client failed", "error", err)
		os.Exit(1)
	}

	var smsClient sms.Client
	switch cfg.SMSProvider {
	case "twilio":
		smsClient = twilio.NewClient(cfg.TwilioAccountSID, cfg.TwilioAuthToken, cfg.TwilioFromNumber, logger)
	default:
		smsClient = telnyx.NewClient(cfg.TelnyxBaseURL, cfg.TelnyxAPIKey, cfg.TelnyxFromNumber, logger)
	}
	scorer := learning.NewCounterScorer(store, logger)
	sugLog := learning.NewSuggestionLog(sqlDB, logger)
	cmdHandler := commands.New(store, smsClient, scorer, sugLog, logger)

	srv := server.New(cfg.TelnyxPublicKey, cfg.TwilioAuthToken, cfg.TwilioWebhookURL, cfg.AlertPhoneNumbers, cmdHandler, logger)
	scan := scanner.New(cfg, kalshiClient, smsClient, store, scorer, sugLog, logger, srv.SetHealthy)

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("http server listening", "port", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			stop()
		}
	}()

	scan.Run(runCtx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "error", err)
	}
	logger.Info("shutdown complete")
}
