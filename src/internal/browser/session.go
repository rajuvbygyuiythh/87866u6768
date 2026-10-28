package browser

// Session injection: replay the localStorage dump captured at registration
// into a cookie-less/fresh browser so the site accepts the account as a
// returning user — the reference project's UseSavedSession feature. The dump
// lives at data/sessions/<uid>.storage.json (saveLocalStorage, profile.go);
// the user object alone (sessions/<uid>.json) is the fallback payload.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"thundr/internal/store"
)

// SavedSessionEntries loads everything we can replay for uid: first the full
// localStorage dump, falling back to the bare user object under key "user".
// A valid payload must contain a user with a hashed_token — otherwise the
// injection could never log in.
func SavedSessionEntries(st *store.Store, uid string) map[string]string {
	entries := map[string]string{}
	if raw, ok := st.SessionStorage(uid); ok {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			for k, v := range m {
				if k != "" {
					entries[k] = v
				}
			}
		}
	}
	if len(entries) == 0 {
		if raw, ok := st.Session(uid); ok {
			var u map[string]any
			if json.Unmarshal([]byte(raw), &u) == nil && u["hashed_token"] != nil {
				entries["user"] = strings.TrimSpace(raw)
			}
		}
	}
	return entries
}

// InjectSessionJS builds the same-origin replay expression. JSON is a subset
// of JS, so embedding the marshalled map is safe — no string-literal escaping.
func InjectSessionJS(entries map[string]string) (string, error) {
	payload, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return `(() => { try { const m = ` + string(payload) + `; let n = 0; ` +
		`for (const [k, v] of Object.entries(m)) { localStorage.setItem(k, typeof v === "string" ? v : JSON.stringify(v)); n++; } ` +
		`return n; } catch (e) { return -1; } })()`, nil
}

// InjectSession writes every saved localStorage key into the CURRENT page. The
// page must be on https://thundr.com already (same-origin storage rule) —
// RunVisibleBot calls it right after a failed login check. Returns false when
// nothing was written.
func InjectSession(ctx context.Context, entries map[string]string) bool {
	if len(entries) == 0 {
		return false
	}
	js, err := InjectSessionJS(entries)
	if err != nil {
		return false
	}
	var n int
	if err := runTO(ctx, 8*time.Second, chromedp.Evaluate(js, &n)); err != nil || n <= 0 {
		return false
	}
	return true
}

// RestoreSession = inject + reload + verify the token survived the app boot.
// Reference-project semantics: a lost/expired profile no longer means manual
// login — the saved session IS the login.
func RestoreSession(ctx context.Context, st *store.Store, uid string, log func(string)) bool {
	entries := SavedSessionEntries(st, uid)
	if len(entries) == 0 {
		log("no saved session dump to inject")
		return false
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	shown := keys
	if len(shown) > 5 {
		shown = append(shown[:5:5], "…")
	}
	log(fmt.Sprintf("injecting saved session (%d key(s): %s)", len(entries), strings.Join(shown, ", ")))
	if !InjectSession(ctx, entries) {
		log("session injection failed (page not on thundr or storage blocked)")
		return false
	}
	// reload so the SPA boots with the new storage
	_ = eval(ctx, `location.href='https://thundr.com/'`, nil)
	if !pollJS(ctx, `(() => { try { return !!JSON.parse(localStorage.getItem('user')||'{}').hashed_token } catch(e){ return false } })()`, 15*time.Second) {
		log("session did not take (page reloaded but no token)")
		return false
	}
	log("session restored - logged in via saved localStorage")
	return true
}
