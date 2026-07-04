package telnyx

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// InboundMessage is the relevant subset of a message.received webhook payload.
type InboundMessage struct {
	From    string
	To      string
	Text    string
	Channel string
}

type webhookEnvelope struct {
	Data struct {
		EventType string `json:"event_type"`
		Payload   struct {
			From struct {
				PhoneNumber string `json:"phone_number"`
			} `json:"from"`
			To []struct {
				PhoneNumber string `json:"phone_number"`
			} `json:"to"`
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"payload"`
	} `json:"data"`
}

// VerifySignature checks the telnyx-signature-ed25519 header against the
// Ed25519 signature of "<timestamp>|<body>" using the account public key
// (base64-encoded, from Mission Control Portal > Keys & Credentials). Events
// older than maxAge are rejected to prevent replay.
func VerifySignature(publicKeyB64, timestamp, signatureB64 string, body []byte, maxAge time.Duration) error {
	if publicKeyB64 == "" {
		return fmt.Errorf("telnyx public key not configured")
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid webhook timestamp %q", timestamp)
	}
	if age := time.Since(time.Unix(ts, 0)); age > maxAge || age < -maxAge {
		return fmt.Errorf("webhook timestamp outside allowed window (age %s)", age)
	}

	pubKey, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return fmt.Errorf("decoding telnyx public key: %w", err)
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("telnyx public key has wrong length %d", len(pubKey))
	}

	signature, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("decoding webhook signature: %w", err)
	}

	signed := append([]byte(timestamp+"|"), body...)
	if !ed25519.Verify(ed25519.PublicKey(pubKey), signed, signature) {
		return fmt.Errorf("webhook signature mismatch")
	}
	return nil
}

// ParseInbound decodes a webhook body and returns the inbound message if the
// event is a message.received event, or nil otherwise.
func ParseInbound(body []byte) (*InboundMessage, error) {
	var env webhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decoding webhook event: %w", err)
	}
	if env.Data.EventType != "message.received" {
		return nil, nil
	}
	msg := &InboundMessage{
		From:    env.Data.Payload.From.PhoneNumber,
		Text:    env.Data.Payload.Text,
		Channel: env.Data.Payload.Type,
	}
	if len(env.Data.Payload.To) > 0 {
		msg.To = env.Data.Payload.To[0].PhoneNumber
	}
	return msg, nil
}
