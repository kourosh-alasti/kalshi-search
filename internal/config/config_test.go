package config

import "testing"

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"user@example.com", "user@example.com", false},
		{" User@Example.COM ", "user@example.com", false},
		{"Name <user@example.com>", "user@example.com", false},
		{"not-an-email", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeEmail(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeEmail(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeEmail(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"4155551234", "+14155551234", false},
		{"415-555-1234", "+14155551234", false},
		{"(415) 555-1234", "+14155551234", false},
		{"14155551234", "+14155551234", false},
		{"+14155551234", "+14155551234", false},
		{"+442071234567", "+442071234567", false},
		{"555123", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := NormalizePhone(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizePhone(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePhone(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
