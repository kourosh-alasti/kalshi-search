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
