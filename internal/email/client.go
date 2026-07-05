// Package email sends alert messages through UseSend (https://usesend.com).
package email

import (
	"context"
	"fmt"
	"log/slog"

	usesend "github.com/usesend/usesend-go"
)

// Client sends plain-text alert emails via UseSend.
type Client struct {
	client  *usesend.Client
	from    string
	subject string
	replyTo []string
	logger  *slog.Logger
}

// NewClient returns a UseSend-backed client. from is the verified sender address;
// subject is used for every alert email.
func NewClient(apiKey, baseURL, from, subject string, replyTo []string, logger *slog.Logger) (*Client, error) {
	opts := []usesend.ClientOption{}
	if baseURL != "" {
		opts = append(opts, usesend.WithBaseURL(baseURL))
	}
	uc, err := usesend.NewClient(apiKey, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating usesend client: %w", err)
	}
	return &Client{
		client:  uc,
		from:    from,
		subject: subject,
		replyTo: replyTo,
		logger:  logger.With("component", "email"),
	}, nil
}

// SendSMS implements sms.Client. The recipient address is an email; body is sent
// as the plain-text part.
func (c *Client) SendSMS(ctx context.Context, to, body string) error {
	payload := usesend.SendEmailPayload{
		To:      []string{to},
		From:    c.from,
		Subject: c.subject,
		Text:    body,
	}
	if len(c.replyTo) > 0 {
		payload.ReplyTo = c.replyTo
	}

	resp, errResp, err := c.client.Emails.Send(ctx, payload)
	if err != nil {
		return fmt.Errorf("sending email to %s: %w", to, err)
	}
	if errResp != nil {
		return fmt.Errorf("usesend error sending to %s: %s (%s)", to, errResp.Message, errResp.Code)
	}

	c.logger.InfoContext(ctx, "email sent",
		"to", to,
		"email_id", resp.EmailID,
		"chars", len(body))
	return nil
}
