// kalshi-verify is a diagnostic tool: it authenticates against the live
// Kalshi API with your configured credentials, fetches open events and
// portfolio positions, and prints what the scanner would see. It sends no
// SMS and writes no state.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/scanner"
)

func main() {
	// Only Kalshi credentials matter here; stub the rest if unset.
	channel := os.Getenv("NOTIFY_CHANNEL")
	if channel == "" {
		channel = "sms"
	}
	stubs := [][2]string{
		{"LIBSQL_URL", "http://127.0.0.1:8080"},
		{"PUBLIC_BASE_URL", "http://127.0.0.1:8080"},
		{"LIBSQL_AUTH_TOKEN", "verify-stub-secret"},
	}
	switch channel {
	case "email":
		stubs = append(stubs,
			[2]string{"NOTIFY_CHANNEL", "email"},
			[2]string{"ALERT_EMAIL", "alerts@example.com"},
			[2]string{"USESEND_API_KEY", "unused"},
			[2]string{"USESEND_FROM_EMAIL", "alerts@example.com"},
		)
	default:
		provider := os.Getenv("SMS_PROVIDER")
		if provider == "" {
			provider = "telnyx"
		}
		stubs = append(stubs, [2]string{"ALERT_PHONE_NUMBER", "+10000000000"})
		if provider == "twilio" {
			stubs = append(stubs,
				[2]string{"TWILIO_ACCOUNT_SID", "unused"},
				[2]string{"TWILIO_AUTH_TOKEN", "unused"},
				[2]string{"TWILIO_FROM_NUMBER", "+10000000000"},
				[2]string{"TWILIO_WEBHOOK_URL", "https://example.com/webhooks/twilio"},
			)
		} else {
			stubs = append(stubs,
				[2]string{"TELNYX_API_KEY", "unused"},
				[2]string{"TELNYX_FROM_NUMBER", "+10000000000"},
			)
		}
	}
	for _, kv := range stubs {
		if os.Getenv(kv[0]) == "" {
			os.Setenv(kv[0], kv[1])
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	client, err := kalshi.NewClient(cfg.KalshiBaseURL, cfg.KalshiAPIKeyID, cfg.KalshiPrivateKeyPEM, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	events, err := client.ListOpenEvents(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listing events FAILED:", err)
		os.Exit(1)
	}
	totalMarkets := 0
	for _, ev := range events {
		totalMarkets += len(ev.Markets)
	}
	fmt.Printf("OK events: %d open events, %d markets, fetched in %s\n", len(events), totalMarkets, time.Since(start).Round(time.Second))

	menu := scanner.CategoriesFromEvents(events)
	fmt.Printf("OK categories (%d):\n%s", len(menu), scanner.FormatCategoryMenu(menu))

	// Show what would qualify with current filter settings, plus how many
	// markets pass each individual filter so thresholds can be tuned.
	now := time.Now()
	qualifying := 0
	passPrice, passVolume, passOI, passClose := 0, 0, 0, 0
	for _, ev := range events {
		for _, m := range ev.Markets {
			price := m.YesAsk > 0 && m.YesAsk <= cfg.MaxPriceCents
			vol := m.Volume >= cfg.MinVolume
			oi := m.OpenInterest >= cfg.MinOpenInterest
			cl := m.CloseTime.After(now) && m.CloseTime.Before(now.Add(time.Duration(cfg.CloseWithinHours)*time.Hour))
			if price {
				passPrice++
			}
			if vol {
				passVolume++
			}
			if oi {
				passOI++
			}
			if cl {
				passClose++
			}
			if price && vol && oi && cl {
				qualifying++
				if qualifying <= 10 {
					fmt.Printf("  match: [%s] %s | %s | YES %d¢ vol=%d oi=%d closes %s\n",
						ev.Category, ev.Title, m.Ticker, m.YesAsk, m.Volume, m.OpenInterest,
						m.CloseTime.Local().Format("Jan 2 15:04"))
				}
			}
		}
	}
	fmt.Printf("filter pass counts: price=%d volume=%d open_interest=%d close_time=%d\n",
		passPrice, passVolume, passOI, passClose)
	fmt.Printf("OK filters: %d markets would qualify (before category filter and dedup)\n", qualifying)

	positions, err := client.ListPositions(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listing positions FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("OK positions: %d market positions in portfolio\n", len(positions))
}
