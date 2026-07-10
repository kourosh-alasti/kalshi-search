package sign

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFeedbackRoundTrip(t *testing.T) {
	s, err := New("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	link := s.FeedbackURL("https://app.example.com", "+14155551234", 7, "took", time.Hour)

	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/feedback/"), "/")
	if len(parts) != 2 {
		t.Fatalf("unexpected path: %s", u.Path)
	}
	userEnc := parts[0]
	suggestionID, err := strconv.Atoi(parts[1])
	if err != nil || suggestionID != 7 {
		t.Fatalf("unexpected suggestion id in path: %s", parts[1])
	}

	q := u.Query()
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.VerifyFeedback(userEnc, suggestionID, q.Get("action"), exp, q.Get("sig"))
	if err != nil {
		t.Fatal(err)
	}
	if user != "+14155551234" {
		t.Fatalf("got user %q", user)
	}
}

func TestToggleRoundTrip(t *testing.T) {
	s, err := New("secret")
	if err != nil {
		t.Fatal(err)
	}
	link := s.ToggleURL("https://x.com", "a@b.com", "enable", time.Hour)

	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	userEnc := strings.TrimPrefix(u.Path, "/toggle/")
	q := u.Query()
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.VerifyToggle(userEnc, q.Get("action"), exp, q.Get("sig"))
	if err != nil {
		t.Fatal(err)
	}
	if user != "a@b.com" {
		t.Fatalf("got user %q", user)
	}
}

func TestEnhancedRoundTrip(t *testing.T) {
	s, err := New("secret")
	if err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"on", "off"} {
		link := s.EnhancedURL("https://x.com", "+14155551234", action, time.Hour)
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		userEnc := strings.TrimPrefix(u.Path, "/enhanced/")
		q := u.Query()
		exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		user, err := s.VerifyEnhanced(userEnc, q.Get("action"), exp, q.Get("sig"))
		if err != nil {
			t.Fatalf("action %s: %v", action, err)
		}
		if user != "+14155551234" {
			t.Fatalf("action %s: got user %q", action, user)
		}
	}
}

func TestVerifyEnhancedExpired(t *testing.T) {
	s, _ := New("secret")
	u := encodeUser("+14155551234")
	_, err := s.VerifyEnhanced(u, "on", time.Now().Add(-time.Hour).Unix(), "bad")
	if err == nil {
		t.Fatal("expected expired link error")
	}
}

func TestVerifyEnhancedBadSignature(t *testing.T) {
	s, _ := New("secret")
	u := encodeUser("+14155551234")
	exp := time.Now().Add(time.Hour).Unix()
	_, err := s.VerifyEnhanced(u, "on", exp, "not-a-valid-signature")
	if err == nil {
		t.Fatal("expected invalid signature error")
	}
}

func TestVerifyEnhancedInvalidAction(t *testing.T) {
	s, _ := New("secret")
	link := s.EnhancedURL("https://x.com", "a@b.com", "on", time.Hour)
	u, _ := url.Parse(link)
	userEnc := strings.TrimPrefix(u.Path, "/enhanced/")
	q := u.Query()
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	_, err := s.VerifyEnhanced(userEnc, "toggle", exp, q.Get("sig"))
	if err == nil {
		t.Fatal("expected invalid action error")
	}
}
