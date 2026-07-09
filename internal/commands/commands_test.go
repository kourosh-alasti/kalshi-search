package commands

import "testing"

func TestWatchRegex(t *testing.T) {
	cases := []struct {
		text string
		ok   bool
	}{
		{"WATCH KXNFL-24-BAL", true},
		{"watch foo 25", true},
		{"WATCH", false},
	}
	for _, tc := range cases {
		if watchRe.MatchString(tc.text) != tc.ok {
			t.Fatalf("%q: expected %v", tc.text, tc.ok)
		}
	}
}

func TestFeedbackRegex(t *testing.T) {
	if !feedbackRe.MatchString("TOOK 12") {
		t.Fatal("should match TOOK 12")
	}
	if !feedbackRe.MatchString("pass #3") {
		t.Fatal("should match pass #3")
	}
}
