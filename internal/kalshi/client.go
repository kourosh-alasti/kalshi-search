// Package kalshi implements a read-only client for the Kalshi Trade API v2.
//
// Every request is signed with RSA-PSS (SHA-256) over the string
// "<timestamp_ms><METHOD><path>", where path excludes query parameters, per
// https://docs.kalshi.com/getting_started/api_keys.
package kalshi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const apiPrefix = "/trade-api/v2"

// Client is a signed, rate-limit-aware Kalshi API client.
type Client struct {
	baseURL    string
	keyID      string
	privateKey *rsa.PrivateKey
	httpClient *http.Client
	logger     *slog.Logger
}

// NewClient parses the PEM private key and returns a ready client.
func NewClient(baseURL, keyID, privateKeyPEM string, logger *slog.Logger) (*Client, error) {
	key, err := parsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parsing Kalshi private key: %w", err)
	}
	return &Client{
		baseURL:    baseURL,
		keyID:      keyID,
		privateKey: key,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger.With("component", "kalshi"),
	}, nil
}

func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("key is neither PKCS#1 nor PKCS#8: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("PKCS#8 key is not an RSA key")
	}
	return rsaKey, nil
}

// sign produces the KALSHI-ACCESS-SIGNATURE value for a request.
func (c *Client) sign(timestampMS, method, path string) (string, error) {
	msg := timestampMS + method + path
	digest := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPSS(rand.Reader, c.privateKey, crypto.SHA256, digest[:], &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// get performs a signed GET request to path (relative to /trade-api/v2) with
// the given query parameters, decoding the JSON response into out. Retries on
// 429 and transient 5xx errors with exponential backoff.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	fullPath := apiPrefix + path
	reqURL := c.baseURL + fullPath
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	const maxAttempts = 4
	backoff := time.Second
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			backoff *= 2
		}

		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		signature, err := c.sign(timestamp, http.MethodGet, fullPath)
		if err != nil {
			return fmt.Errorf("signing request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("KALSHI-ACCESS-KEY", c.keyID)
		req.Header.Set("KALSHI-ACCESS-TIMESTAMP", timestamp)
		req.Header.Set("KALSHI-ACCESS-SIGNATURE", signature)
		req.Header.Set("Accept", "application/json")

		start := time.Now()
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			c.logger.WarnContext(ctx, "kalshi request failed", "path", path, "attempt", attempt, "error", err)
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		latency := time.Since(start)

		c.logger.DebugContext(ctx, "kalshi http call",
			"method", "GET", "path", path, "status", resp.StatusCode,
			"latency_ms", latency.Milliseconds(), "attempt", attempt, "bytes", len(body))

		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("decoding %s response: %w", path, err)
			}
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("kalshi %s returned %d: %s", path, resp.StatusCode, truncate(string(body), 300))
			c.logger.WarnContext(ctx, "kalshi transient error, will retry",
				"path", path, "status", resp.StatusCode, "attempt", attempt)
			continue
		default:
			return fmt.Errorf("kalshi %s returned %d: %s", path, resp.StatusCode, truncate(string(body), 300))
		}
	}
	return fmt.Errorf("kalshi %s failed after %d attempts: %w", path, maxAttempts, lastErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ListOpenEvents fetches all open events with their nested markets, following
// cursor pagination until exhausted.
func (c *Client) ListOpenEvents(ctx context.Context) ([]Event, error) {
	var events []Event
	cursor := ""
	for page := 1; ; page++ {
		q := url.Values{
			"status":              {"open"},
			"with_nested_markets": {"true"},
			"limit":               {"200"},
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp eventsResponse
		if err := c.get(ctx, "/events", q, &resp); err != nil {
			return nil, fmt.Errorf("listing events page %d: %w", page, err)
		}
		events = append(events, resp.Events...)
		c.logger.DebugContext(ctx, "fetched events page", "page", page, "events", len(resp.Events), "total", len(events))
		if resp.Cursor == "" || len(resp.Events) == 0 {
			break
		}
		cursor = resp.Cursor
		// Stay well under Kalshi's read rate limits when paginating.
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return events, nil
}

// ListTagsByCategories returns Kalshi's category-to-tag mapping used for
// subcategory filtering.
func (c *Client) ListTagsByCategories(ctx context.Context) (map[string][]string, error) {
	var resp tagsByCategoriesResponse
	if err := c.get(ctx, "/search/tags_by_categories", nil, &resp); err != nil {
		return nil, fmt.Errorf("listing tags by categories: %w", err)
	}
	if resp.TagsByCategories == nil {
		return map[string][]string{}, nil
	}
	return resp.TagsByCategories, nil
}

// GetSeries returns metadata for one series ticker, including tags.
func (c *Client) GetSeries(ctx context.Context, seriesTicker string) (Series, error) {
	var resp seriesResponse
	path := "/series/" + url.PathEscape(seriesTicker)
	if err := c.get(ctx, path, nil, &resp); err != nil {
		return Series{}, fmt.Errorf("fetching series %s: %w", seriesTicker, err)
	}
	return resp.Series, nil
}

// ListPositions fetches all current market positions, following cursor
// pagination until exhausted.
func (c *Client) ListPositions(ctx context.Context) ([]MarketPosition, error) {
	var positions []MarketPosition
	cursor := ""
	for page := 1; ; page++ {
		q := url.Values{"limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp positionsResponse
		if err := c.get(ctx, "/portfolio/positions", q, &resp); err != nil {
			return nil, fmt.Errorf("listing positions page %d: %w", page, err)
		}
		positions = append(positions, resp.MarketPositions...)
		if resp.Cursor == "" || len(resp.MarketPositions) == 0 {
			break
		}
		cursor = resp.Cursor
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return positions, nil
}
