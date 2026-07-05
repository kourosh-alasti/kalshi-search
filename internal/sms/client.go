// Package sms defines the outbound messaging interface shared by scanner and
// commands. SMS providers live in internal/telnyx and internal/twilio; email
// alerts use internal/email (UseSend).
package sms

import "context"

// Client sends alert messages to a recipient address (E.164 phone or email).
type Client interface {
	SendSMS(ctx context.Context, to, body string) error
}
