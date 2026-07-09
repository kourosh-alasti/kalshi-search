package sign

import (
	"testing"
	"time"
)

func TestFeedbackRoundTrip(t *testing.T) {
	s, err := New("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	link := s.FeedbackURL("https://app.example.com", "+14155551234", 7, "took", time.Hour)
	// /feedback/{user}/7?action=took&exp=...&sig=...
	const prefix = "https://app.example.com/feedback/"
	if len(link) <= len(prefix) {
		t.Fatalf("unexpected link: %s", link)
	}
	rest := link[len(prefix):]
	slash := 0
	for i, c := range rest {
		if c == '/' {
			slash = i
			break
		}
	}
	userEnc := rest[:slash]
	// parse query manually
	qIdx := len(link) - 1
	for i := len(link) - 1; i >= 0; i-- {
		if link[i] == '?' {
			qIdx = i
			break
		}
	}
	query := link[qIdx+1:]
	vals := map[string]string{}
	for _, part := range splitQuery(query) {
		kv := splitKV(part)
		if len(kv) == 2 {
			vals[kv[0]] = kv[1]
		}
	}
	exp, _ := parseInt64(vals["exp"])
	user, err := s.VerifyFeedback(userEnc, 7, vals["action"], exp, vals["sig"])
	if err != nil {
		t.Fatal(err)
	}
	if user != "+14155551234" {
		t.Fatalf("got user %q", user)
	}
}

func splitQuery(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '&' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitKV(s string) []string {
	for i, c := range s {
		if c == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

func TestToggleRoundTrip(t *testing.T) {
	s, _ := New("secret")
	link := s.ToggleURL("https://x.com", "a@b.com", "enable", time.Hour)
	if link == "" {
		t.Fatal("empty link")
	}
}
