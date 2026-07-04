// Package telnyx implements a client for the Telnyx v2 messaging API and
// verification of its inbound webhooks.
package telnyx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client sends SMS messages through Telnyx.
type Client struct {
	baseURL    string
	apiKey     string
	fromNumber string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewClient returns a Telnyx API client sending from the given E.164 number.
func NewClient(baseURL, apiKey, fromNumber string, logger *slog.Logger) *Client {
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		fromNumber: fromNumber,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger.With("component", "telnyx"),
	}
}

type sendRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	Type string `json:"type"`
}

// SendSMS sends body as an SMS to the given E.164 phone number.
func (c *Client) SendSMS(ctx context.Context, to, body string) error {
	raw, err := json.Marshal(sendRequest{
		From: c.fromNumber,
		To:   to,
		Text: body,
		Type: "SMS",
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/messages", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending sms: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	c.logger.InfoContext(ctx, "telnyx send",
		"status", resp.StatusCode,
		"latency_ms", time.Since(start).Milliseconds(),
		"chars", len(body))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telnyx returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
