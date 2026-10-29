package browser

// Ban detection for visible mode, ported from the reference project's
// browser/ban.go: thundr shows a suspension page; when we see it the account
// is dead — stop the worker loudly instead of looping forever.

import (
	"context"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// BannedError marks an account suspension — the caller auto-removes the
// account from the store (same contract as the reference project's BannedError).
type BannedError struct{ Reason string }

func (e BannedError) Error() string { return "account banned: " + e.Reason }

// CheckBanned reports whether the current page is a suspension screen;
// returns the human reason when banned.
func CheckBanned(ctx context.Context) (string, bool) {
	var href, body string
	_ = runTO(ctx, 5*time.Second, chromedp.Location(&href))
	_ = runTO(ctx, 5*time.Second, chromedp.Evaluate(
		`(document.body && document.body.innerText || '').slice(0, 3000)`, &body))

	low := strings.ToLower(href + "\n" + body)
	if !strings.Contains(low, "banned") && !strings.Contains(low, "suspend") {
		return "", false
	}

	// pull readable fields (reference: Ban ID / Reason / Remaining labels)
	reason := ""
	for _, label := range []string{"Reason", "reason"} {
		if i := strings.Index(body, label); i >= 0 {
			rest := strings.TrimSpace(body[i+len(label):])
			rest = strings.TrimLeft(rest, ": \t")
			if end := strings.IndexAny(rest, "\r\n"); end > 0 {
				rest = rest[:end]
			}
			if rest != "" {
				reason = rest
				break
			}
		}
	}
	if reason == "" {
		if strings.Contains(low, "account suspended") {
			reason = "account suspended"
		} else {
			reason = "account banned"
		}
	}
	return reason, true
}
