// Package config loads and validates all service configuration from
// environment variables. The service has no other configuration surface.
package config

import (
	"fmt"
	"log/slog"
	"net/mail"
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

	// Notification channel: sms or email (mutually exclusive).
	NotifyChannel string

	// SMS provider: telnyx or twilio (required when NotifyChannel=sms).
	SMSProvider string

	// Telnyx credentials.
	TelnyxAPIKey     string
	TelnyxFromNumber string
	TelnyxPublicKey  string // base64 Ed25519 key for webhook verification
	TelnyxBaseURL    string

	// Twilio credentials.
	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFromNumber string
	TwilioWebhookURL string // full public URL for webhook signature verification

	// Alert recipients: E.164 phone numbers (sms) or email addresses (email).
	AlertRecipients []string

	// UseSend credentials (required when NotifyChannel=email).
	UseSendAPIKey     string
	UseSendFromEmail  string
	UseSendBaseURL    string
	UseSendSubject    string
	UseSendReplyTo    []string

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
	PublicBaseURL       string
	OnboardingTokenTTL  time.Duration

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

		TwilioAccountSID: os.Getenv("TWILIO_ACCOUNT_SID"),
		TwilioAuthToken:  os.Getenv("TWILIO_AUTH_TOKEN"),
		TwilioFromNumber: os.Getenv("TWILIO_FROM_NUMBER"),
		TwilioWebhookURL: os.Getenv("TWILIO_WEBHOOK_URL"),

		UseSendAPIKey:    os.Getenv("USESEND_API_KEY"),
		UseSendFromEmail: os.Getenv("USESEND_FROM_EMAIL"),
		UseSendBaseURL:   os.Getenv("USESEND_BASE_URL"),

		LibSQLURL:       os.Getenv("LIBSQL_URL"),
		LibSQLAuthToken: os.Getenv("LIBSQL_AUTH_TOKEN"),
		Port:            getEnv("PORT", "8080"),
		PublicBaseURL:   strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
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

	if cfg.KalshiAPIKeyID == "" {
		errs = append(errs, "KALSHI_API_KEY_ID is required")
	}

	cfg.NotifyChannel = strings.ToLower(getEnv("NOTIFY_CHANNEL", "sms"))
	switch cfg.NotifyChannel {
	case "sms", "email":
	default:
		errs = append(errs, "NOTIFY_CHANNEL must be sms or email")
	}

	hasPhone := strings.TrimSpace(os.Getenv("ALERT_PHONE_NUMBER")) != ""
	hasEmail := strings.TrimSpace(os.Getenv("ALERT_EMAIL")) != ""
	if hasPhone && hasEmail {
		errs = append(errs, "set either ALERT_PHONE_NUMBER or ALERT_EMAIL, not both")
	}

	switch cfg.NotifyChannel {
	case "sms":
		if !hasPhone {
			errs = append(errs, "ALERT_PHONE_NUMBER is required when NOTIFY_CHANNEL=sms")
		} else if hasEmail {
			errs = append(errs, "ALERT_EMAIL must not be set when NOTIFY_CHANNEL=sms")
		} else if err := cfg.loadAlertPhones(&errs); err != nil {
			return nil, err
		}
	case "email":
		if !hasEmail {
			errs = append(errs, "ALERT_EMAIL is required when NOTIFY_CHANNEL=email")
		} else if hasPhone {
			errs = append(errs, "ALERT_PHONE_NUMBER must not be set when NOTIFY_CHANNEL=email")
		} else if err := cfg.loadAlertEmails(&errs); err != nil {
			return nil, err
		}
	}

	if cfg.NotifyChannel == "sms" {
		cfg.SMSProvider = strings.ToLower(getEnv("SMS_PROVIDER", "telnyx"))
		switch cfg.SMSProvider {
		case "telnyx":
			if cfg.TelnyxAPIKey == "" {
				errs = append(errs, "TELNYX_API_KEY is required when NOTIFY_CHANNEL=sms")
			}
			if cfg.TelnyxFromNumber == "" {
				errs = append(errs, "TELNYX_FROM_NUMBER is required when NOTIFY_CHANNEL=sms")
			}
		case "twilio":
			if cfg.TwilioAccountSID == "" {
				errs = append(errs, "TWILIO_ACCOUNT_SID is required when NOTIFY_CHANNEL=sms")
			}
			if cfg.TwilioAuthToken == "" {
				errs = append(errs, "TWILIO_AUTH_TOKEN is required when NOTIFY_CHANNEL=sms")
			}
			if cfg.TwilioFromNumber == "" {
				errs = append(errs, "TWILIO_FROM_NUMBER is required when NOTIFY_CHANNEL=sms")
			}
			if cfg.TwilioWebhookURL == "" {
				errs = append(errs, "TWILIO_WEBHOOK_URL is required when NOTIFY_CHANNEL=sms")
			}
		default:
			errs = append(errs, "SMS_PROVIDER must be telnyx or twilio")
		}
	}

	if cfg.NotifyChannel == "email" {
		if cfg.UseSendAPIKey == "" {
			errs = append(errs, "USESEND_API_KEY is required when NOTIFY_CHANNEL=email")
		}
		if cfg.UseSendFromEmail == "" {
			errs = append(errs, "USESEND_FROM_EMAIL is required when NOTIFY_CHANNEL=email")
		} else if _, err := NormalizeEmail(cfg.UseSendFromEmail); err != nil {
			errs = append(errs, fmt.Sprintf("USESEND_FROM_EMAIL: %v", err))
		}
		cfg.UseSendSubject = getEnv("USESEND_SUBJECT", "Kalshi Alerts")
		if raw := os.Getenv("USESEND_REPLY_TO"); raw != "" {
			for _, part := range strings.Split(raw, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				addr, err := NormalizeEmail(part)
				if err != nil {
					errs = append(errs, fmt.Sprintf("USESEND_REPLY_TO entry %q: %v", part, err))
					continue
				}
				cfg.UseSendReplyTo = append(cfg.UseSendReplyTo, addr)
			}
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
	if cfg.OnboardingTokenTTL, err = getEnvDuration("ONBOARDING_TOKEN_TTL", 24*time.Hour); err != nil {
		errs = append(errs, err.Error())
	}

	if cfg.PublicBaseURL == "" {
		errs = append(errs, "PUBLIC_BASE_URL is required (e.g. https://your-app.up.railway.app)")
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

// NormalizeEmail validates and normalizes an email address.
func NormalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("email address is required")
	}
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", fmt.Errorf("invalid email address")
	}
	return strings.ToLower(addr.Address), nil
}

func (cfg *Config) loadAlertPhones(errs *[]string) error {
	raw := os.Getenv("ALERT_PHONE_NUMBER")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		num, err := NormalizePhone(part)
		if err != nil {
			*errs = append(*errs, fmt.Sprintf("ALERT_PHONE_NUMBER entry %q: %v", part, err))
			continue
		}
		cfg.AlertRecipients = append(cfg.AlertRecipients, num)
	}
	if len(cfg.AlertRecipients) == 0 && len(*errs) == 0 {
		*errs = append(*errs, "ALERT_PHONE_NUMBER contains no valid numbers")
	}
	return nil
}

func (cfg *Config) loadAlertEmails(errs *[]string) error {
	raw := os.Getenv("ALERT_EMAIL")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, err := NormalizeEmail(part)
		if err != nil {
			*errs = append(*errs, fmt.Sprintf("ALERT_EMAIL entry %q: %v", part, err))
			continue
		}
		cfg.AlertRecipients = append(cfg.AlertRecipients, addr)
	}
	if len(cfg.AlertRecipients) == 0 && len(*errs) == 0 {
		*errs = append(*errs, "ALERT_EMAIL contains no valid addresses")
	}
	return nil
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
