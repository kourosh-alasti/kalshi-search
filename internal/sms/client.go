// Package sms defines the outbound messaging interface shared by scanner and
// commands. Concrete providers live in internal/telnyx and internal/twilio.
package sms

import "context"

// Client sends SMS messages to E.164 phone numbers.
type Client interface {
	SendSMS(ctx context.Context, to, body string) error
}
