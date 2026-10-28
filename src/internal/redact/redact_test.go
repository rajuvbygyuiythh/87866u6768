package redact

import "testing"

func TestRedactMasksSecrets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"api key sk-abc123def456ghi789xyz ok", "api key sk-a*** ok"},
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.token", "Authorization: Bearer ***"},
		{`save password="hunter42" now`, `save password="***" now`},
		{"hashed_token:abcdef1234567890", "hashed_token:***"},
		{"proxy http://user:secretpw@1.2.3.4:8080 down", "proxy http://user:***@1.2.3.4:8080 down"},
		{"ai_api_key = sk-featherless-key-987654321", "ai_api_key = sk-f***"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactKeepsNormalLines(t *testing.T) {
	keep := []string{
		"MATCHED a61bfb40-cf06-4622-9a5c-accb77a48c97 after 3.5s shared=2",
		"registering thr_abc12345",
		"extracted a61bfb40 | user@example.com",
		"settings verify: privacy.hidden=true",
		"worker a61bfb40 -> port=9222 proxy=direct",
		"plain line nothing secret",
	}
	for _, line := range keep {
		if got := Redact(line); got != line {
			t.Errorf("Redact changed a clean line: %q -> %q", line, got)
		}
	}
}