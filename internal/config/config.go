// Package config loads and validates all service configuration from
// environment variables. The service has no other configuration surface.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable for the service. See .env.example / README for
// documentation of each variable.
type Config struct {
	// Kalshi API credentials.
	KalshiAPIKeyID      string
	KalshiPrivateKeyPEM string // PEM contents (takes precedence)
	KalshiBaseURL       string

	// Telnyx credentials.
	TelnyxAPIKey     string
	TelnyxFromNumber string
	TelnyxPublicKey  string // base64 Ed25519 key for webhook verification
	TelnyxBaseURL    string

	// Alerting targets, normalized to E.164.
	AlertPhoneNumbers []string

	// Scanner filters.
	MaxPriceCents       int
	MinVolume           int
	MinOpenInterest     int
	CloseWithinHours    int
	PollInterval        time.Duration
	RealertDropCents    int
	MaxAlertsPerMessage int

	// Learning.
	MinSuggestionScore    float64
	LearnExpandCategories bool

	// Persistence (libSQL).
	LibSQLURL       string
	LibSQLAuthToken string

	// HTTP server.
	Port string

	// Logging.
	LogLevel slog.Level
}

// Load reads configuration from the environment, applying defaults and
// validating required values. It returns a descriptive error listing every
// missing required variable rather than failing on the first one.
func Load() (*Config, error) {
	cfg := &Config{
		KalshiAPIKeyID:      os.Getenv("KALSHI_API_KEY_ID"),
		KalshiPrivateKeyPEM: os.Getenv("KALSHI_API_PRIVATE_KEY"),
		KalshiBaseURL:       getEnv("KALSHI_BASE_URL", "https://api.elections.kalshi.com"),

		TelnyxAPIKey:     os.Getenv("TELNYX_API_KEY"),
		TelnyxFromNumber: os.Getenv("TELNYX_FROM_NUMBER"),
		TelnyxPublicKey:  os.Getenv("TELNYX_PUBLIC_KEY"),
		TelnyxBaseURL:    getEnv("TELNYX_BASE_URL", "https://api.telnyx.com"),

		LibSQLURL:       os.Getenv("LIBSQL_URL"),
		LibSQLAuthToken: os.Getenv("LIBSQL_AUTH_TOKEN"),
		Port:            getEnv("PORT", "8080"),
	}

	var errs []string

	// Private key: env var contents win, else read from file path.
	if cfg.KalshiPrivateKeyPEM == "" {
		keyPath := getEnv("KALSHI_PRIVATE_KEY_PATH", "certificates/kalshi-private-key.pem")
		if raw, err := os.ReadFile(keyPath); err == nil {
			cfg.KalshiPrivateKeyPEM = string(raw)
		} else {
			errs = append(errs, fmt.Sprintf("KALSHI_API_PRIVATE_KEY not set and could not read KALSHI_PRIVATE_KEY_PATH %q: %v", keyPath, err))
		}
	}

	if cfg.LibSQLURL == "" {
		errs = append(errs, "LIBSQL_URL is required")
	}

	for name, val := range map[string]string{
		"KALSHI_API_KEY_ID":  cfg.KalshiAPIKeyID,
		"TELNYX_API_KEY":     cfg.TelnyxAPIKey,
		"TELNYX_FROM_NUMBER": cfg.TelnyxFromNumber,
	} {
		if val == "" {
			errs = append(errs, fmt.Sprintf("%s is required", name))
		}
	}

	if raw := os.Getenv("ALERT_PHONE_NUMBER"); raw == "" {
		errs = append(errs, "ALERT_PHONE_NUMBER is required")
	} else {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			num, err := NormalizePhone(part)
			if err != nil {
				errs = append(errs, fmt.Sprintf("ALERT_PHONE_NUMBER entry %q: %v", part, err))
				continue
			}
			cfg.AlertPhoneNumbers = append(cfg.AlertPhoneNumbers, num)
		}
		if len(cfg.AlertPhoneNumbers) == 0 && len(errs) == 0 {
			errs = append(errs, "ALERT_PHONE_NUMBER contains no valid numbers")
		}
	}

	var err error
	if cfg.MaxPriceCents, err = getEnvInt("MAX_PRICE_CENTS", 30); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.MinVolume, err = getEnvInt("MIN_VOLUME", 1000); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.MinOpenInterest, err = getEnvInt("MIN_OPEN_INTEREST", 500); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.CloseWithinHours, err = getEnvInt("CLOSE_WITHIN_HOURS", 72); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.RealertDropCents, err = getEnvInt("REALERT_DROP_CENTS", 5); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.MaxAlertsPerMessage, err = getEnvInt("MAX_ALERTS_PER_MESSAGE", 5); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.PollInterval, err = getEnvDuration("POLL_INTERVAL", time.Minute); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.MinSuggestionScore, err = getEnvFloat("MIN_SUGGESTION_SCORE", 0); err != nil {
		errs = append(errs, err.Error())
	}
	if cfg.LearnExpandCategories, err = getEnvBool("LEARN_EXPAND_CATEGORIES", false); err != nil {
		errs = append(errs, err.Error())
	}

	if cfg.MaxPriceCents < 1 || cfg.MaxPriceCents > 99 {
		errs = append(errs, "MAX_PRICE_CENTS must be between 1 and 99")
	}

	switch strings.ToLower(getEnv("LOG_LEVEL", "info")) {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn", "warning":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		errs = append(errs, "LOG_LEVEL must be one of debug|info|warn|error")
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// NormalizePhone converts a phone number to E.164, assuming US (+1) for
// bare 10-digit numbers like "4155551234".
func NormalizePhone(s string) (string, error) {
	hadPlus := strings.HasPrefix(strings.TrimSpace(s), "+")
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	switch {
	case hadPlus && len(d) >= 8:
		return "+" + d, nil
	case len(d) == 10:
		return "+1" + d, nil
	case len(d) == 11 && d[0] == '1':
		return "+" + d, nil
	default:
		return "", fmt.Errorf("expected a 10-digit US number or E.164 format")
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, v)
	}
	return n, nil
}

func getEnvFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", key, v)
	}
	return f, nil
}

func getEnvBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean, got %q", key, v)
	}
	return b, nil
}

func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 60s or 2m, got %q", key, v)
	}
	if d < 5*time.Second {
		return 0, fmt.Errorf("%s must be at least 5s", key)
	}
	return d, nil
}
