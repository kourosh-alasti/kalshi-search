package twilio

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// InboundMessage is the relevant subset of an inbound SMS webhook payload.
type InboundMessage struct {
	From string
	To   string
	Text string
}

// VerifySignature checks the X-Twilio-Signature header against the HMAC-SHA1
// of the webhook URL concatenated with sorted POST parameter key/value pairs.
// webhookURL must be the exact public URL configured in the Twilio console.
func VerifySignature(authToken, webhookURL, signature string, params url.Values) error {
	if authToken == "" {
		return fmt.Errorf("twilio auth token not configured")
	}
	if webhookURL == "" {
		return fmt.Errorf("twilio webhook URL not configured")
	}
	if signature == "" {
		return fmt.Errorf("missing X-Twilio-Signature header")
	}

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(webhookURL)
	for _, key := range keys {
		b.WriteString(key)
		b.WriteString(params.Get(key))
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("webhook signature mismatch")
	}
	return nil
}

// ParseInbound decodes a form-encoded webhook body and returns the inbound
// message when From and Body are present.
func ParseInbound(body []byte) (*InboundMessage, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("decoding webhook form: %w", err)
	}

	from := values.Get("From")
	text := values.Get("Body")
	if from == "" || text == "" {
		return nil, nil
	}

	return &InboundMessage{
		From: from,
		To:   values.Get("To"),
		Text: text,
	}, nil
}
