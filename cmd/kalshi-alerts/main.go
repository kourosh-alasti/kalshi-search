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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kourosh/kalshi-search/internal/commands"
	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/db"
	"github.com/kourosh/kalshi-search/internal/email"
	"github.com/kourosh/kalshi-search/internal/inbound"
	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/leader"
	"github.com/kourosh/kalshi-search/internal/learning"
	"github.com/kourosh/kalshi-search/internal/metrics"
	"github.com/kourosh/kalshi-search/internal/scanstats"
	"github.com/kourosh/kalshi-search/internal/scanner"
	"github.com/kourosh/kalshi-search/internal/server"
	"github.com/kourosh/kalshi-search/internal/sign"
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
		"enabled", cfg.Enabled,
		"instance_id", cfg.InstanceID,
		"leader_election", cfg.LeaderElection,
		"notify_channel", cfg.NotifyChannel,
		"poll_interval", cfg.PollInterval.String())

	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.LibSQLURL, cfg.LibSQLAuthToken)
	if err != nil {
		logger.Error("opening libsql failed", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	store := state.Open(sqlDB, logger)
	if cfg.Enabled {
		for _, recipient := range cfg.AlertRecipients {
			if err := store.EnsureUser(ctx, recipient); err != nil {
				logger.Error("creating user failed", "recipient", recipient, "error", err)
				os.Exit(1)
			}
		}
	}

	var msgClient sms.Client
	if cfg.Enabled {
		switch cfg.NotifyChannel {
		case "email":
			var err error
			msgClient, err = email.NewClient(cfg.UseSendAPIKey, cfg.UseSendBaseURL, cfg.UseSendFromEmail, cfg.UseSendSubject, cfg.UseSendReplyTo, logger)
			if err != nil {
				logger.Error("creating email client failed", "error", err)
				os.Exit(1)
			}
		default:
			switch cfg.SMSProvider {
			case "twilio":
				msgClient = twilio.NewClient(cfg.TwilioAccountSID, cfg.TwilioAuthToken, cfg.TwilioFromNumber, logger)
			default:
				msgClient = telnyx.NewClient(cfg.TelnyxBaseURL, cfg.TelnyxAPIKey, cfg.TelnyxFromNumber, logger)
			}
		}
	} else {
		msgClient = sms.NewNoop(logger)
		logger.Warn("scanner and notifications disabled (ENABLED=false)")
	}

	secret := cfg.LinkSigningSecret
	if secret == "" && !cfg.Enabled {
		secret = "disabled"
	}
	signer, err := sign.New(secret)
	if err != nil {
		logger.Error("creating link signer failed", "error", err)
		os.Exit(1)
	}

	stats := &scanstats.Tracker{}
	inboundQ := inbound.New(sqlDB, logger)
	scorer := learning.NewPreferenceScorer(store, sqlDB, cfg.MLMinTrainingExamples, logger)
	sugLog := learning.NewSuggestionLog(sqlDB, logger)
	cmdHandler := commands.New(cfg, store, msgClient, scorer, sugLog, signer, logger)

	var isLeader atomic.Bool
	isLeader.Store(!cfg.LeaderElection)
	isLeaderFn := func() bool { return isLeader.Load() }

	srv := server.New(cfg, cmdHandler, store, inboundQ, signer, stats, logger)

	var scan *scanner.Scanner
	if cfg.Enabled {
		kalshiClient, err := kalshi.NewClient(cfg.KalshiBaseURL, cfg.KalshiAPIKeyID, cfg.KalshiPrivateKeyPEM, logger)
		if err != nil {
			logger.Error("creating kalshi client failed", "error", err)
			os.Exit(1)
		}
		scan = scanner.New(cfg, kalshiClient, msgClient, store, scorer, sugLog, inboundQ,
			cmdHandler, signer, stats, logger, srv.SetHealthy, isLeaderFn)
	}

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.MetricsEnabled {
		metrics.Register()
	}

	if cfg.Enabled && cfg.LeaderElection {
		elector := leader.New(sqlDB, cfg.InstanceID, 30*time.Second, logger)
		go elector.RunLoop(runCtx, 10*time.Second, func(leader bool) {
			isLeader.Store(leader)
			metrics.LeaderGauge.Set(boolFloat(leader))
			if stats != nil {
				stats.SetLeader(leader)
			}
			if leader {
				logger.Info("became scanner leader", "instance", cfg.InstanceID)
			} else {
				logger.Info("relinquished scanner leadership", "instance", cfg.InstanceID)
			}
		})
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(cfg.MetricsEnabled),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("http server listening", "port", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			stop()
		}
	}()

	if scan != nil {
		scan.Run(runCtx)
	} else {
		<-runCtx.Done()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "error", err)
	}
	logger.Info("shutdown complete")
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
