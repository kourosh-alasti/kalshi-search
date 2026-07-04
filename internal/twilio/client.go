// Package twilio implements a client for the Twilio Messaging API and
// verification of its inbound webhooks.
package twilio

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client sends SMS messages through Twilio.
type Client struct {
	accountSID string
	authToken  string
	fromNumber string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewClient returns a Twilio API client sending from the given E.164 number.
func NewClient(accountSID, authToken, fromNumber string, logger *slog.Logger) *Client {
	return &Client{
		accountSID: accountSID,
		authToken:  authToken,
		fromNumber: fromNumber,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger.With("component", "twilio"),
	}
}

// SendSMS sends body as an SMS to the given E.164 phone number.
func (c *Client) SendSMS(ctx context.Context, to, body string) error {
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", c.accountSID)
	form := url.Values{
		"From": {c.fromNumber},
		"To":   {to},
		"Body": {body},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.accountSID, c.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending sms: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	c.logger.InfoContext(ctx, "twilio send",
		"status", resp.StatusCode,
		"latency_ms", time.Since(start).Milliseconds(),
		"chars", len(body))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("twilio returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
