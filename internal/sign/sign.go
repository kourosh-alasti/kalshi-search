// Package sign provides HMAC-signed action links for email feedback and toggles.
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Signer creates and verifies signed URLs scoped to a user recipient.
type Signer struct {
	secret []byte
}

// New returns a signer using the given secret (must be non-empty).
func New(secret string) (*Signer, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, fmt.Errorf("link signing secret is required")
	}
	return &Signer{secret: []byte(secret)}, nil
}

func (s *Signer) mac(parts ...string) string {
	h := hmac.New(sha256.New, s.secret)
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte("|"))
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func encodeUser(user string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(user))
}

func decodeUser(encoded string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid user encoding")
	}
	return string(raw), nil
}

// FeedbackURL builds a signed link for TOOK/PASS feedback on a suggestion.
func (s *Signer) FeedbackURL(baseURL, user string, suggestionID int, action string, ttl time.Duration) string {
	action = strings.ToLower(strings.TrimSpace(action))
	exp := time.Now().Add(ttl).Unix()
	u := encodeUser(user)
	sig := s.mac("feedback", u, action, strconv.Itoa(suggestionID), strconv.FormatInt(exp, 10))
	return fmt.Sprintf("%s/feedback/%s/%d?action=%s&exp=%d&sig=%s",
		strings.TrimRight(baseURL, "/"), u, suggestionID, action, exp, sig)
}

// VerifyFeedback checks a feedback link and returns the user, suggestion id, and action.
func (s *Signer) VerifyFeedback(userEnc string, suggestionID int, action string, exp int64, sig string) (string, error) {
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("link expired")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "took" && action != "pass" {
		return "", fmt.Errorf("invalid action")
	}
	want := s.mac("feedback", userEnc, action, strconv.Itoa(suggestionID), strconv.FormatInt(exp, 10))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", fmt.Errorf("invalid signature")
	}
	user, err := decodeUser(userEnc)
	if err != nil {
		return "", err
	}
	return user, nil
}

// ToggleURL builds a signed link to enable or disable quiet hours.
func (s *Signer) ToggleURL(baseURL, user, toggle string, ttl time.Duration) string {
	toggle = strings.ToLower(strings.TrimSpace(toggle))
	exp := time.Now().Add(ttl).Unix()
	u := encodeUser(user)
	sig := s.mac("toggle", u, toggle, strconv.FormatInt(exp, 10))
	return fmt.Sprintf("%s/toggle/%s?action=%s&exp=%d&sig=%s",
		strings.TrimRight(baseURL, "/"), u, toggle, exp, sig)
}

// VerifyToggle checks a toggle link.
func (s *Signer) VerifyToggle(userEnc, toggle string, exp int64, sig string) (string, error) {
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("link expired")
	}
	toggle = strings.ToLower(strings.TrimSpace(toggle))
	if toggle != "enable" && toggle != "disable" {
		return "", fmt.Errorf("invalid action")
	}
	want := s.mac("toggle", userEnc, toggle, strconv.FormatInt(exp, 10))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", fmt.Errorf("invalid signature")
	}
	return decodeUser(userEnc)
}

// PrefsURL builds a signed preferences link (reuses onboarding flow with token param).
func (s *Signer) PrefsURL(baseURL, token string) string {
	return fmt.Sprintf("%s/onboard/%s", strings.TrimRight(baseURL, "/"), url.PathEscape(token))
}

// EnhancedURL builds a signed link to opt in or out of ML-enhanced suggestions.
func (s *Signer) EnhancedURL(baseURL, user, action string, ttl time.Duration) string {
	action = strings.ToLower(strings.TrimSpace(action))
	exp := time.Now().Add(ttl).Unix()
	u := encodeUser(user)
	sig := s.mac("enhanced", u, action, strconv.FormatInt(exp, 10))
	return fmt.Sprintf("%s/enhanced/%s?action=%s&exp=%d&sig=%s",
		strings.TrimRight(baseURL, "/"), u, action, exp, sig)
}

// VerifyEnhanced checks an enhanced-suggestions toggle link.
func (s *Signer) VerifyEnhanced(userEnc, action string, exp int64, sig string) (string, error) {
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("link expired")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "on" && action != "off" {
		return "", fmt.Errorf("invalid action")
	}
	want := s.mac("enhanced", userEnc, action, strconv.FormatInt(exp, 10))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", fmt.Errorf("invalid signature")
	}
	return decodeUser(userEnc)
}
