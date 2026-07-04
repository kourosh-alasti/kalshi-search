package twilio

import (
	"net/url"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	// Vector from https://www.twilio.com/docs/usage/security#validating-requests
	authToken := "12345"
	webhookURL := "https://example.com/myapp.php?foo=1&bar=2"
	params := url.Values{
		"CallSid": {"CA1234567890ABCDE"},
		"Caller":  {"+14158675310"},
		"Digits":  {"1234"},
		"From":    {"+14158675310"},
		"To":      {"+18005551212"},
	}
	signature := "L/OH5YylLD5NRKLltdqwSvS0BnU="

	if err := VerifySignature(authToken, webhookURL, signature, params); err != nil {
		t.Fatalf("VerifySignature() error = %v", err)
	}
}

func TestParseInbound(t *testing.T) {
	body := []byte("From=%2B14155551234&To=%2B14155559876&Body=LIST")
	msg, err := ParseInbound(body)
	if err != nil {
		t.Fatalf("ParseInbound() error = %v", err)
	}
	if msg == nil {
		t.Fatal("ParseInbound() = nil, want message")
	}
	if msg.From != "+14155551234" {
		t.Errorf("From = %q, want +14155551234", msg.From)
	}
	if msg.To != "+14155559876" {
		t.Errorf("To = %q, want +14155559876", msg.To)
	}
	if msg.Text != "LIST" {
		t.Errorf("Text = %q, want LIST", msg.Text)
	}
}

func TestParseInboundMissingFields(t *testing.T) {
	msg, err := ParseInbound([]byte("From=%2B14155551234"))
	if err != nil {
		t.Fatalf("ParseInbound() error = %v", err)
	}
	if msg != nil {
		t.Fatalf("ParseInbound() = %+v, want nil", msg)
	}
}
