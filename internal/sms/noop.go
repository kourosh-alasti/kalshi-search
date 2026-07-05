package sms

import (
	"context"
	"log/slog"
)

// Noop discards outbound messages. Used when ENABLED=false.
type Noop struct {
	logger *slog.Logger
}

// NewNoop returns a client that logs and drops messages.
func NewNoop(logger *slog.Logger) *Noop {
	return &Noop{logger: logger.With("component", "sms", "noop", true)}
}

// SendSMS implements Client.
func (n *Noop) SendSMS(ctx context.Context, to, body string) error {
	n.logger.Info("notification suppressed", "to", to, "body_len", len(body))
	return nil
}
