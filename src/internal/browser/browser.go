// Package browser is the Go port of register_browser.py + browser_bot.py:
// real, visible Chrome driven over CDP so hCaptcha solves itself.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"thundr/internal/bot"
	"thundr/internal/reply"
	"thundr/internal/store"
)

// Callbacks report bot progress to the dashboard.
type Callbacks struct {
	Log      func(uid, state, msg string)
	Status   func(uid string, matches, replies, line int)
	MatchEnd func(uid, cls string, why []string) // partner classified bot/real/unknown
	Share    func(uid, platform string)          // final (share) line sent: snap|tg|both
}

// delays mirrors the human_delay settings (settings.txt).
type delays struct {
	on      bool
	typeMs  int
	jitter  int
	clickMs int
	submit  int
}

func loadDelays(st *store.Store) delays {
	return delays{
		on:      st.Bool("human_delay", true),
		typeMs:  st.Int("type_delay_ms", 40),
		jitter:  st.Int("type_jitter_ms", 60),
		clickMs: st.Int("click_delay_ms", 300),
		submit:  st.Int("submit_delay_ms", 1000),
	}
}

func (d delays) sleep(ms int) {
	if d.on && ms > 0 {
		time.Sleep(time.Duration(float64(ms)*(0.7+rand.Float64()*0.6)) * time.Millisecond)
	}
}

func (d delays) typeDelay() time.Duration {
	if !d.on {
		return 5 * time.Millisecond
	}
	return time.Duration(d.typeMs+rand.Intn(d.jitter+1)) * time.Millisecond
}

// ---- launch ------------------------------------------------------------------

// AttachURL, when non-empty, switches every browser launch to ATTACH mode:
// the bot connects over DevTools to a Chrome the USER started (shortcut with
// --remote-debugging-port). Needed on machines where security policy kills
// chrome processes spawned by unsigned binaries.
var AttachURL string

func newVisibleChrome(ctx context.Context, proxy, userDataDir string) (context.Context, context.CancelFunc) {
	return newVisibleChromeOn(ctx, proxy, userDataDir, AttachURL)
}

// newVisibleChromeOn attaches to (or spawns) a visible chrome. attachURL
// non-empty = attach mode over DevTools (multi-browser: each worker has its
// own URL/port); empty = spawn (only where policy allows).
func newVisibleChromeOn(ctx context.Context, proxy, userDataDir, attachURL string) (context.Context, context.CancelFunc) {
	if attachURL != "" {
		allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, attachURL)
		// Chrome is launched with thundr.com as its start page — attach to THAT
		// existing tab instead of opening a new one, so the user never sees a
		// leftover blank tab.
		listCtx, cancelList := chromedp.NewContext(allocCtx)
		targets, err := chromedp.Targets(listCtx)
		cancelList()
		if err == nil {
			for _, t := range targets {
				if t.Type == "page" && strings.Contains(t.URL, "thundr.com") {
					tabCtx, cancelTab := chromedp.NewContext(allocCtx, chromedp.WithTargetID(t.TargetID))
					return tabCtx, func() {
						cancelTab()
						cancelAlloc()
					}
				}
			}
		}
		browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
		return browserCtx, func() {
			cancelBrowser()
			cancelAlloc()
		}
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", true),
		// hide the automation tell: navigator.webdriver === true otherwise
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// keep profiles lean (storage-based, low RAM)
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
	)
	if userDataDir != "" {
		opts = append(opts, chromedp.UserDataDir(userDataDir))
	}
	if proxy != "" {
		opts = append(opts, chromedp.ProxyServer(proxy))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	return browserCtx, func() {
		cancelBrowser()
		cancelAlloc()
	}
}

// proxyGeo resolves the proxy exit IP's timezone/locale for fingerprint binding.
func proxyGeo(proxy string) (tz, country string) {
	// fingerprinted hop (uTLS + proxy): a Go ClientHello on the geo probe would
	// hand the datacenter/automation signal right back on every launch.
	client := bot.ChromeHTTPClient(proxy)
	resp, err := client.Get("https://ipapi.co/json/")
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var geo struct {
		Timezone string `json:"timezone"`
		Country  string `json:"country_code"`
	}
	if json.Unmarshal(body, &geo) != nil {
		return "", ""
	}
	return geo.Timezone, geo.Country
}

// bindProxyLocale aligns the browser's timezone/locale with the proxy exit
// (a timezone/IP mismatch is a classic datacenter-bot fingerprint).
func bindProxyLocale(ctx context.Context, proxy string, log func(string)) string {
	tz, country := proxyGeo(proxy)
	if tz == "" {
		return ""
	}
	_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := emulation.SetTimezoneOverride(tz).Do(ctx); err != nil {
			return err
		}
		locale := "en-US"
		if country != "" && country != "US" {
			locale = "en-" + country
		}
		return emulation.SetLocaleOverride().WithLocale(locale).Do(ctx)
	}))
	log(fmt.Sprintf("proxy bound: timezone=%s country=%s", tz, country))
	return tz
}

// captureFingerprint records what the site can see, incl. its protections.
func captureFingerprint(ctx context.Context, st *store.Store, uid, proxyTz string, log func(string)) {
	var fp map[string]any
	_ = eval(ctx, `({
		ua: navigator.userAgent, webdriver: navigator.webdriver,
		lang: navigator.language, langs: navigator.languages, platform: navigator.platform,
		cores: navigator.hardwareConcurrency, mem: navigator.deviceMemory || null,
		tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
		tzOffset: new Date().getTimezoneOffset(),
		screen: [screen.width, screen.height, screen.colorDepth],
		plugins: navigator.plugins.length, cookies: navigator.cookieEnabled,
		hcaptcha_script: !!document.querySelector('script[src*="hcaptcha"]'),
		cloudflare: !!document.querySelector('script[src*="cloudflare"], script[src*="challenge"]')
	})`, &fp)
	if fp == nil {
		return
	}
	fp["proxy_timezone"] = proxyTz
	fp["captured_at"] = time.Now().Format(time.RFC3339)
	if raw, err := json.MarshalIndent(fp, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(st.ProfileDir(uid), "fingerprint.json"), raw, 0o644)
	}
	log(fmt.Sprintf("fingerprint: webdriver=%v tz=%v (proxy-tz=%q) hcaptcha=%v cloudflare=%v",
		fp["webdriver"], fp["tz"], proxyTz, fp["hcaptcha_script"], fp["cloudflare"]))
}

// humanMouse wanders the cursor like a person (CDP trusted events).
func humanMouse(ctx context.Context) {
	x, y := 200.0+rand.Float64()*800, 200.0+rand.Float64()*400
	steps := 4 + rand.Intn(5)
	for i := 0; i < steps; i++ {
		x += (rand.Float64() - 0.5) * 300
		y += (rand.Float64() - 0.5) * 200
		if x < 10 {
			x = 10
		}
		if y < 10 {
			y = 10
		}
		_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx)
		}))
		time.Sleep(time.Duration(60+rand.Intn(140)) * time.Millisecond)
	}
}

// warmup visits 3-5 random real sites like a human BEFORE touching thundr:
// builds cookies/history so the profile doesn't look born-5-seconds-ago.
func warmup(ctx context.Context, d delays, log func(string)) {
	// Simplified warmup - visit just one site to save time
	log("warmup: visiting one site like a human")
	if err := runTO(ctx, 25*time.Second, chromedp.Navigate("https://www.bbc.com/news")); err != nil {
		log("warmup: bbc.com failed (skip)")
		return
	}
	log("warmup: https://www.bbc.com/news")
	time.Sleep(3 * time.Second) // Brief dwell
	humanMouse(ctx)
	log("warmup done - profile has real cookies/history now")
}

// initJS restores the saved session and wraps window.WebSocket so Go can drain
// frames and send through the app's own socket (same approach as browser_bot.py).
const initJS = `
try { const u = localStorage.getItem('__saved_user'); if (u) localStorage.setItem('user', u); } catch (e) {}
window.__frames = [];
window.__ws = null;
const O = window.WebSocket;
const Wrapped = function(url, proto) {
  const ws = proto !== undefined ? new O(url, proto) : new O(url);
  window.__ws = ws;
  ws.addEventListener('message', e => { try { window.__frames.push(String(e.data)); } catch (err) {} });
  return ws;
};
Wrapped.prototype = O.prototype;
Wrapped.CONNECTING = O.CONNECTING; Wrapped.OPEN = O.OPEN;
Wrapped.CLOSING = O.CLOSING; Wrapped.CLOSED = O.CLOSED;
window.WebSocket = Wrapped;
window.__drain = () => { const f = window.__frames; window.__frames = []; return f; };
window.__send = d => { if (window.__ws && window.__ws.readyState === 1) { window.__ws.send(d); return true; } return false; };
`

func addInitScript(script string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(script).Do(ctx)
		return err
	})
}

// ---- small DOM helpers (JS so :has-text / aria all work) ----------------------

// runTO runs actions with a hard timeout so a non-ready element or a page
// mid-navigation can never hang the bot loop.
func runTO(ctx context.Context, d time.Duration, actions ...chromedp.Action) error {
	tctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return chromedp.Run(tctx, actions...)
}

func eval(ctx context.Context, js string, out any) error {
	return runTO(ctx, 6*time.Second, chromedp.Evaluate(js, out))
}

func clickText(ctx context.Context, text string) bool {
	var ok bool
	js := `(() => { const b=[...document.querySelectorAll('button')].find(e => (e.textContent||'').trim().includes(` + strconv.Quote(text) + `)); if(b){b.click();return true} return false })()`
	_ = eval(ctx, js, &ok)
	return ok
}

func clickAria(ctx context.Context, label string) bool {
	var ok bool
	js := `(() => { const b=document.querySelector('button[aria-label=' + JSON.stringify(` + strconv.Quote(label) + `) + ']'); if(b){b.click();return true} return false })()`
	_ = eval(ctx, js, &ok)
	return ok
}

// clickSiteButton finds a visible button by text/aria-label regex and clicks
// it with a REAL mouse click — the site's React ignores synthetic .click().
// This makes the action visible in the browser window AND server-authoritative.
// clickStartButton clicks the site's Start control wherever it renders: the
// home page (aria-label "Start Chatting") OR the idle chat page (a "Start Esc"
// text button whose aria-label is NOT "Start Chatting").
func clickStartButton(ctx context.Context) bool {
	if exists(ctx, `button[aria-label="Start Chatting"]`) {
		if err := runTO(ctx, 6*time.Second,
			chromedp.Click(`button[aria-label="Start Chatting"]`, chromedp.ByQuery)); err != nil {
			clickAria(ctx, "Start Chatting")
		}
		return true
	}
	return clickSiteButton(ctx, `^start\b`)
}

func clickSiteButton(ctx context.Context, pattern string) bool {
	var found string
	js := `(() => {
		const re = new RegExp(` + strconv.Quote(pattern) + `, 'i');
		const btns = [...document.querySelectorAll('button, [role="button"]')]
			.filter(b => b.offsetParent !== null);
		const b = btns.find(b => re.test((b.innerText || '').trim()) || re.test(b.getAttribute('aria-label') || ''));
		if (!b) return '';
		b.scrollIntoView({block: 'center'});
		const r = b.getBoundingClientRect();
		return JSON.stringify({x: r.x + r.width / 2, y: r.y + r.height / 2});
	})()`
	if err := runTO(ctx, 3*time.Second, chromedp.Evaluate(js, &found)); err != nil || found == "" {
		return false
	}
	var pt struct {
		X, Y float64
	}
	if json.Unmarshal([]byte(found), &pt) != nil {
		return false
	}
	return runTO(ctx, 3*time.Second, chromedp.MouseClickXY(pt.X, pt.Y)) == nil
}

func exists(ctx context.Context, sel string) bool {
	var ok bool
	_ = eval(ctx, `!!document.querySelector(`+strconv.Quote(sel)+`)`, &ok)
	return ok
}

func waitURLContains(ctx context.Context, part string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var href string
		_ = eval(ctx, `location.href`, &href)
		if strings.Contains(href, part) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func selectByText(ctx context.Context, text string) bool {
	// poll: the select for the next step renders a beat after the Next click
	for i := 0; i < 15; i++ {
		var ok bool
		js := `(() => { const s=[...document.querySelectorAll('select')].find(s=>[...s.options].some(o=>o.label===` + strconv.Quote(text) + `||o.text===` + strconv.Quote(text) + `)); if(!s)return false; const o=[...s.options].find(o=>o.label===` + strconv.Quote(text) + `||o.text===` + strconv.Quote(text) + `); s.value=o.value; s.dispatchEvent(new Event('change',{bubbles:true})); s.dispatchEvent(new Event('input',{bubbles:true})); return true })()`
		_ = eval(ctx, js, &ok)
		if ok {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

// pollJS retries a JS predicate until true or timeout.
func pollJS(ctx context.Context, js string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var ok bool
		if eval(ctx, js, &ok) == nil && ok {
			return true
		}
		time.Sleep(350 * time.Millisecond)
	}
	return false
}

// setBirthday applies the persona's DOB (21-23yo, matching the "f 21" chat lines).
func setBirthday(ctx context.Context, year string) bool {
	return pollJS(ctx, `(() => { const s=document.querySelectorAll('select'); if(s.length<3)return false; const set=(i,t)=>{const el=s[i];if(!el)return false;const o=[...el.options].find(o=>o.label===t||o.text===t);if(!o)return false;el.value=o.value;el.dispatchEvent(new Event('change',{bubbles:true}));el.dispatchEvent(new Event('input',{bubbles:true}));return el.value===o.value}; return set(0,'January')&&set(1,'15')&&set(2,`+strconv.Quote(year)+`) })()`, 8*time.Second)
}

// humanFill clicks a field then types with per-keystroke latency.
func humanFill(ctx context.Context, sel, text string, d delays) error {
	if !exists(ctx, sel) {
		return fmt.Errorf("field not found: %s", sel)
	}
	if err := runTO(ctx, 10*time.Second, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		return err
	}
	d.sleep(80 + rand.Intn(120))
	for _, r := range text {
		if err := runTO(ctx, 5*time.Second, chromedp.SendKeys(sel, string(r), chromedp.ByQuery)); err != nil {
			return err
		}
		time.Sleep(d.typeDelay())
	}
	d.sleep(100 + rand.Intn(150))
	return nil
}

// ---- registration (port of register_browser.py) --------------------------------

func genCreds() (string, string) {
	const lower = "abcdefghijklmnopqrstuvwxyz0123456789"
	const all = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	u := "thr_"
	for i := 0; i < 8; i++ {
		u += string(lower[rand.Intn(len(lower))])
	}
	p := make([]byte, 12)
	for i := range p {
		p[i] = all[rand.Intn(len(all))]
	}
	return u, string(p)
}

// RegisterAccounts registers count accounts, each in its OWN persistent Chrome
// profile (browser-profiles/<uid>) so the app sees a real returning user later.
//
// Profile rules (this is what makes "next run uses the SAME browser" work):
//   - every account gets a FRESH profile dir, never --guest. A guest session
//     keeps cookies/localStorage in memory and writes nothing to disk, so an
//     old guest registration left no reusable browser behind.
//   - the dir the chrome process actually writes to is the dir that gets
//     renamed onto browser-profiles/<uid> (the old attach path launched
//     reg-guest and renamed reg-live, which saved an empty directory).
//   - chrome is closed over CDP BEFORE the rename (Windows locks the dir).
//   - the UA is NOT overridden at registration: the real installed Chrome
//     identity is captured into the fingerprint and replayed on every later
//     run, so the profile and the UA can never disagree.
func RegisterAccounts(ctx context.Context, st *store.Store, count int, proxy string, log func(string)) []store.Account {
	d := loadDelays(st)
	attach := strings.TrimSpace(st.Get("chrome_attach_url", "")) != ""
	root := rootOf(st)
	var results []store.Account

	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return results
		default:
		}

		// one fresh persistent profile per account
		tmpDir := filepath.Join(root, "browser-profiles", fmt.Sprintf("reg-%d-%d", time.Now().UnixNano(), i))
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			log("profile dir create failed: " + err.Error())
			continue
		}

		attachURL := ""
		if attach {
			// attach mode: the watcher/user chrome owns this profile dir.
			// Anything still listening on the register port is a leftover chrome
			// with a DIFFERENT profile - close it first, or the registration
			// would write into that profile instead of tmpDir.
			attachURL = RegisterURL(st)
			CloseChromeOn(attachURL)
			waitPortDown(attachURL, 10*time.Second)
			if !ensureChromeOn(st, tmpDir, attachURL, "", startPage, false, log) {
				log("register chrome did not open - skipping this account")
				_ = os.RemoveAll(tmpDir)
				continue
			}
		}

		browserCtx, cancel := newVisibleChromeOn(ctx, proxy, tmpDir, attachURL)
		if err := chromedp.Run(browserCtx); err != nil {
			log("chrome connect failed: " + err.Error())
			cancel()
			_ = os.RemoveAll(tmpDir)
			continue
		}
		proxyTz := ""
		if proxy != "" {
			proxyTz = bindProxyLocale(browserCtx, proxy, log)
		}
		warmup(browserCtx, d, log) // real cookies/history BEFORE thundr

		acc, storage, ok := registerOne(browserCtx, st, d, i+1, count, log)
		if ok {
			captureFingerprint(browserCtx, st, acc.UserID, proxyTz, log)
		}

		// close the browser BEFORE the dir can be renamed
		if attach {
			closeAttachedBrowser(browserCtx) // user chrome must exit to unlock the profile dir
			waitPortDown(attachURL, 12*time.Second)
		}
		cancel() // fully close chrome BEFORE renaming the profile dir
		if !ok {
			_ = os.RemoveAll(tmpDir)
			continue
		}

		finalDir := st.ProfileDir(acc.UserID)
		saved := moveProfile(tmpDir, finalDir, log)
		if saved {
			saveProfileMeta(st, acc, finalDir, "", proxy, proxyTz, storage, log)
		} else {
			// keep the account (it exists server-side) but say loudly that the
			// browser state was lost - the visible bot cannot reuse it.
			log("!! account " + acc.UserID[:8] + " has no saved profile (visible mode will need a manual login)")
		}
		results = append(results, acc)
	}
	return results
}

// closeAttachedBrowser asks the user-started chrome to quit (CDP Browser.close).
func closeAttachedBrowser(ctx context.Context) {
	_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpbrowser.Close().Do(ctx)
	}))
}

// registerOne drives one /create flow in an already-attached browser and
// returns the account plus a dump of the page's localStorage (the session the
// app itself keeps). The storage map is what makes the saved profile
// verifiable: user object, device id, onboarding flags, feature flags.
func registerOne(ctx context.Context, st *store.Store, d delays, index, total int, log func(string)) (store.Account, map[string]string, bool) {
	username, password := genCreds()
	log(fmt.Sprintf("[%d/%d] registering %s", index, total, username))

	tabCtx, cancelTab := chromedp.NewContext(ctx)
	defer cancelTab()

	if err := chromedp.Run(tabCtx, chromedp.Navigate("https://thundr.com/create")); err != nil {
		log("open /create failed: " + err.Error())
		return store.Account{}, nil, false
	}
	time.Sleep(3 * time.Second)
	humanMouse(tabCtx)

	// 2-7s pause between fields: a human reads/thinks, a bot fills instantly
	pause := func() {
		time.Sleep(time.Duration(2000+rand.Intn(5000)) * time.Millisecond)
	}

	if err := humanFill(tabCtx, `input[placeholder="Choose a unique username"]`, username, d); err != nil {
		log("username fill: " + err.Error())
		return store.Account{}, nil, false
	}
	pause()
	if err := humanFill(tabCtx, `input[placeholder="Choose a password"]`, password, d); err != nil {
		log("password fill: " + err.Error())
		return store.Account{}, nil, false
	}
	pause()
	if err := humanFill(tabCtx, `input[placeholder="Confirm your password"]`, password, d); err != nil {
		log("confirm fill: " + err.Error())
		return store.Account{}, nil, false
	}
	pause()
	humanMouse(tabCtx)
	if err := runTO(tabCtx, 6*time.Second,
		chromedp.Click(`button[aria-label="Create Account"]`, chromedp.ByQuery)); err != nil {
		if !clickAria(tabCtx, "Create Account") {
			log("Create Account button not found")
			return store.Account{}, nil, false
		}
	}
	if !waitURLContains(tabCtx, "/register", 25*time.Second) {
		log("no redirect to /register within 25s")
		return store.Account{}, nil, false
	}
	log("redirected to /register")

	time.Sleep(time.Second)
	// Scroll to bottom to ensure all terms are visible
	_ = eval(tabCtx, `window.scrollTo(0, document.body.scrollHeight)`, nil)
	time.Sleep(1 * time.Second)

	// Check for any checkboxes that need to be checked first
	var checkboxCount int
	_ = eval(tabCtx, `document.querySelectorAll('input[type="checkbox"]').length`, &checkboxCount)
	if checkboxCount > 0 {
		log(fmt.Sprintf("Found %d checkboxes, checking them", checkboxCount))
		// Check all checkboxes
		for i := 0; i < checkboxCount; i++ {
			selector := fmt.Sprintf("input[type=\"checkbox\"]:nth-of-type(%d)", i+1)
			if exists(tabCtx, selector) {
				if err := runTO(tabCtx, 3*time.Second, chromedp.Click(selector, chromedp.ByQuery)); err != nil {
					log(fmt.Sprintf("Failed to click checkbox %d", i+1))
				} else {
					log(fmt.Sprintf("Clicked checkbox %d", i+1))
				}
			}
		}
		time.Sleep(1 * time.Second)
	}

	// trusted CDP click via exact aria-label (BySearch proved unreliable on this SPA)
	if err := runTO(tabCtx, 5*time.Second,
		chromedp.Click(`button[aria-label="I Agree"]`, chromedp.ByQuery)); err != nil {
		log("I Agree aria click failed, trying text search")
		if err2 := runTO(tabCtx, 5*time.Second, chromedp.Click("I Agree", chromedp.BySearch)); err2 != nil {
			clickText(tabCtx, "I Agree")
		}
	}

	log("age gate: I Agree")

	// Wait for the page to actually change after clicking I Agree
	time.Sleep(5 * time.Second)

	// Check if we're still on the age gate page
	var stillOnAgeGate bool
	_ = eval(tabCtx, `document.body.innerText.includes("You must be at least 18 years old")`, &stillOnAgeGate)
	if stillOnAgeGate {
		log("!! Still on age gate page after I Agree click - trying to identify specific elements")

		// Dump more detailed page information to understand what's happening
		var pageDetails map[string]any
		_ = eval(tabCtx, `({
			allButtons: [...document.querySelectorAll('button')].map(b => ({text: b.textContent, aria: b.getAttribute('aria-label'), id: b.id, className: b.className})),
			allInputs: [...document.querySelectorAll('input')].map(i => ({type: i.type, id: i.id, className: i.className})),
			pageTitle: document.title,
			currentURL: location.href
		})`, &pageDetails)

		if raw, err := json.Marshal(pageDetails); err == nil {
			log("Detailed page elements: " + string(raw)[:500] + "...")
		}
	}

	// Add a longer wait to ensure page transition
	time.Sleep(3 * time.Second)

	// persona: 21-23F (consistent with the "hey f 21" chat opener)
	year := fmt.Sprintf("%d", 2003+rand.Intn(3))

	// Wait a bit for the birthday page to load
	time.Sleep(3 * time.Second)

	// Check if we're on the birthday page by looking for selects
	var selectCount int
	_ = eval(tabCtx, `document.querySelectorAll('select').length`, &selectCount)
	if selectCount < 3 {
		log(fmt.Sprintf("!! Only %d selects found, waiting for birthday page to load", selectCount))
		time.Sleep(5 * time.Second)
		_ = eval(tabCtx, `document.querySelectorAll('select').length`, &selectCount)
		log(fmt.Sprintf("After wait, found %d selects", selectCount))
	}

	// birthday step: apply (with retries) and verify Next advances the page
	if selectCount >= 3 {
		if !setBirthday(tabCtx, year) {
			log("!! birthday selects never appeared - DOM dump follows")
			var dump map[string]any
			_ = eval(tabCtx, `({
				url: location.href,
				title: document.title,
				buttons: [...document.querySelectorAll('button')].map(b => ((b.getAttribute('aria-label')||'') + '|' + (b.textContent||'').trim())).slice(0, 20),
				selects: document.querySelectorAll('select').length,
				inputs: [...document.querySelectorAll('input')].map(i => i.placeholder || i.type).slice(0, 15),
				checkboxes: document.querySelectorAll('input[type=checkbox]').length,
				body_text: document.body ? document.body.innerText.slice(0, 400) : ''
			})`, &dump)
			if raw, err := json.Marshal(dump); err == nil {
				log("DOM: " + string(raw))
			}
		} else {
			log("Birthday set successfully")
		}
	} else {
		log("!! Not enough selects for birthday page, skipping birthday step")
	}

	time.Sleep(time.Duration(2000+rand.Intn(3000)) * time.Millisecond)
	for i := 0; i < 3; i++ {
		if err := runTO(tabCtx, 5*time.Second,
			chromedp.Click(`button[aria-label="Next"]`, chromedp.ByQuery)); err != nil {
			log("Next aria click failed, trying text search")
			if err2 := runTO(tabCtx, 5*time.Second, chromedp.Click("Next", chromedp.BySearch)); err2 != nil {
				clickText(tabCtx, "Next")
			}
		}
		time.Sleep(1200 * time.Millisecond)
		if pollJS(tabCtx, `[...document.querySelectorAll('select')].some(s=>[...s.options].some(o=>o.label==='Female'||o.text==='Female'))`, 4*time.Second) {
			log("advanced to sex step")
			break
		}
	}
	log("next -> sex")
	// After clicking Next from birthday, we should be on the sex selection page
	// Wait a bit for the page to load
	time.Sleep(2 * time.Second)

	// Check if we're on the sex selection page by looking for sex-related selects
	var sexSelectExists bool
	_ = eval(tabCtx, `(() => {
		const selects = document.querySelectorAll('select');
		for (let select of selects) {
			const options = [...select.options];
			for (let option of options) {
				if (option.text.includes('Male') || option.text.includes('Female')) {
					return true;
				}
			}
		}
		return false;
	})()`, &sexSelectExists)

	if sexSelectExists {
		log("Found sex selection page")
		if !selectByText(tabCtx, "Female") {
			log("!! sex select failed")
			// Dump page state to understand what's happening
			var dump map[string]any
			_ = eval(tabCtx, `({
				url: location.href,
				title: document.title,
				buttons: [...document.querySelectorAll('button')].map(b => ((b.getAttribute('aria-label')||'') + '|' + (b.textContent||'').trim())).slice(0, 20),
				selects: document.querySelectorAll('select').length,
				selectContents: [...document.querySelectorAll('select')].map(s => [...s.options].map(o => o.text)),
				body_text: document.body ? document.body.innerText.slice(0, 400) : ''
			})`, &dump)
			if raw, err := json.Marshal(dump); err == nil {
				log("Sex page DOM: " + string(raw))
			}

			// Try alternative approach - directly select by index if we know the structure
			log("Trying direct sex selection by index")
			var success bool
			_ = eval(tabCtx, `(() => {
				try {
					const selects = document.querySelectorAll('select');
					for (let select of selects) {
						const options = [...select.options];
						for (let i = 0; i < options.length; i++) {
							if (options[i].text.includes('Female')) {
								select.value = options[i].value;
								select.dispatchEvent(new Event('change', {bubbles: true}));
								select.dispatchEvent(new Event('input', {bubbles: true}));
								return true;
							}
						}
					}
					return false;
				} catch(e) {
					return false;
				}
			})()`, &success)

			if success {
				log("Direct sex selection succeeded")
			} else {
				log("Direct sex selection also failed")
			}
		} else {
			log("Sex selected successfully")
		}
	} else {
		log("No sex selection page detected, assuming automatic progression")
	}
	time.Sleep(time.Duration(2000+rand.Intn(3000)) * time.Millisecond)
	for i := 0; i < 3; i++ {
		if err := runTO(tabCtx, 5*time.Second,
			chromedp.Click(`button[aria-label="Next"]`, chromedp.ByQuery)); err != nil {
			if err2 := runTO(tabCtx, 5*time.Second, chromedp.Click("Next", chromedp.BySearch)); err2 != nil {
				clickText(tabCtx, "Next")
			}
		}
		time.Sleep(1200 * time.Millisecond)
		if pollJS(tabCtx, `[...document.querySelectorAll('select')].some(s=>[...s.options].some(o=>o.label==='Everyone'||o.text==='Everyone'))`, 4*time.Second) {
			log("advanced to looking-for step")
			break
		}
	}
	log("next -> looking for")
	// Wait for the looking-for page to load
	time.Sleep(2 * time.Second)

	// Check if we're on the looking-for selection page
	var lookingForExists bool
	_ = eval(tabCtx, `(() => {
		const selects = document.querySelectorAll('select');
		for (let select of selects) {
			const options = [...select.options];
			for (let option of options) {
				if (option.text.includes('Everyone') || option.text.includes('Male') || option.text.includes('Female')) {
					return true;
				}
			}
		}
		return false;
	})()`, &lookingForExists)

	if lookingForExists {
		if !selectByText(tabCtx, "Everyone") {
			log("!! looking-for select failed")
			// Dump page state to understand what's happening
			var dump map[string]any
			_ = eval(tabCtx, `({
				url: location.href,
				title: document.title,
				buttons: [...document.querySelectorAll('button')].map(b => ((b.getAttribute('aria-label')||'') + '|' + (b.textContent||'').trim())).slice(0, 20),
				selects: document.querySelectorAll('select').length,
				selectContents: [...document.querySelectorAll('select')].map(s => [...s.options].map(o => o.text)),
				body_text: document.body ? document.body.innerText.slice(0, 400) : ''
			})`, &dump)
			if raw, err := json.Marshal(dump); err == nil {
				log("Looking-for page DOM: " + string(raw))
			}

			// Try alternative approach - directly select by index
			log("Trying direct looking-for selection by index")
			var success bool
			_ = eval(tabCtx, `(() => {
				try {
					const selects = document.querySelectorAll('select');
					for (let select of selects) {
						const options = [...select.options];
						for (let i = 0; i < options.length; i++) {
							if (options[i].text.includes('Everyone')) {
								select.value = options[i].value;
								select.dispatchEvent(new Event('change', {bubbles: true}));
								select.dispatchEvent(new Event('input', {bubbles: true}));
								return true;
							}
						}
					}
					return false;
				} catch(e) {
					return false;
				}
			})()`, &success)

			if success {
				log("Direct looking-for selection succeeded")
			} else {
				log("Direct looking-for selection also failed")
			}
		} else {
			log("Looking-for selected successfully")
		}
	} else {
		log("No looking-for selection page detected, assuming automatic progression")
	}
	time.Sleep(time.Duration(2000+rand.Intn(3000)) * time.Millisecond)
	// Debug the finish registration button before clicking
	log("Checking Finish Registration button state")
	var buttonInfo map[string]any
	_ = eval(tabCtx, `(() => {
		const btn = document.querySelector('button[aria-label="Finish Registration"]');
		if (!btn) return {found: false, reason: "not found by aria-label"};
		return {
			found: true,
			visible: btn.offsetParent !== null,
			disabled: btn.disabled,
			text: btn.textContent,
			ariaLabel: btn.getAttribute('aria-label'),
			className: btn.className,
			style: btn.style.cssText
		};
	})()`, &buttonInfo)

	if raw, err := json.Marshal(buttonInfo); err == nil {
		log("Finish button info: " + string(raw))
	}

	finishOK := false
	if buttonInfo["found"] == true {
		log("Finish Registration button found, attempting to click")
		if err := runTO(tabCtx, 6*time.Second,
			chromedp.Click(`button[aria-label="Finish Registration"]`, chromedp.ByQuery)); err == nil {
			finishOK = true
			log("Finish Registration button clicked successfully")
		} else {
			log("Primary Finish Registration click failed, trying alternative approaches")
			// Try alternative approaches
			if err2 := runTO(tabCtx, 6*time.Second, chromedp.Click("Finish Registration", chromedp.BySearch)); err2 == nil {
				finishOK = true
				log("Secondary Finish Registration click succeeded")
			} else {
				log("Secondary Finish Registration click also failed")
				// Try JS click as last resort
				finishOK = clickAria(tabCtx, "Finish Registration")
				if finishOK {
					log("JS click for Finish Registration succeeded")
				} else {
					log("All Finish Registration click attempts failed")
				}
			}
		}
	} else {
		log("Finish Registration button not found, trying to find any finish button")
		// Try to find any button with "Finish" in the text
		var finishButtonExists bool
		_ = eval(tabCtx, `document.querySelector('button') && [...document.querySelectorAll('button')].some(b => b.textContent.includes('Finish'))`, &finishButtonExists)
		if finishButtonExists {
			log("Found button with 'Finish' text, trying to click")
			if err := runTO(tabCtx, 6*time.Second, chromedp.Click("Finish", chromedp.BySearch)); err == nil {
				finishOK = true
				log("Finish button clicked successfully")
			} else {
				log("Finish button click failed")
			}
		} else {
			log("No finish button found at all")
		}
	}
	log(fmt.Sprintf("finish registration (clicked=%v)", finishOK))

	// Wait a bit for registration to complete
	time.Sleep(5 * time.Second)

	// Check if we're on the completion page
	var currentPage string
	_ = eval(tabCtx, `location.href`, &currentPage)
	log(fmt.Sprintf("Current page after finish: %s", currentPage))

	// Additional wait to ensure registration completes
	time.Sleep(5 * time.Second)

	var user map[string]any
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		_ = eval(tabCtx, `JSON.parse(localStorage.getItem('user') || '{}')`, &user)
		if tok, _ := user["hashed_token"].(string); tok != "" {
			break
		}
	}
	if tok, _ := user["hashed_token"].(string); tok == "" {
		var href string
		_ = eval(tabCtx, `location.href`, &href)
		var shot []byte
		_ = runTO(tabCtx, 5*time.Second, chromedp.CaptureScreenshot(&shot))
		_ = os.WriteFile(st.ProfileDir("../reg_debug_go.png"), shot, 0o644)
		log(fmt.Sprintf("no token after 20s (page: %s, screenshot reg_debug_go.png)", href))
	}
	uid, _ := user["id"].(string)
	email, _ := user["email"].(string)
	token, _ := user["hashed_token"].(string)
	if uid == "" || token == "" {
		log("incomplete registration (no hashed_token)")
		return store.Account{}, nil, false
	}
	log(fmt.Sprintf("extracted %s | %s", uid[:8], email))

	applyAccountSettings(tabCtx, log)

	// Capture + persist the account's device fingerprint (reference-project
	// behaviour): replayed later on WS/API so the account keeps one identity.
	if fp, err := CaptureFingerprint(tabCtx); err == nil && fp.UserAgent != "" {
		st.SaveFingerprint(uid, fp)
		log(fmt.Sprintf("fingerprint saved: %s", fp.Summary()))
	}

	st.AppendAccount(uid, email, token)
	st.AppendCreds(username, password, uid, token)
	if raw, err := json.Marshal(user); err == nil {
		st.SaveSession(uid, string(raw))
	}

	// Full localStorage dump: the app's own session state (user object is the
	// important key, but device id / flags / onboarding live next to it).
	storage := map[string]string{}
	_ = eval(tabCtx, `(() => {
		const out = {};
		try {
			for (let i = 0; i < localStorage.length; i++) {
				const k = localStorage.key(i);
				const v = localStorage.getItem(k) || "";
				if (v.length <= 65536) out[k] = v;
			}
		} catch (e) {}
		return out;
	})()`, &storage)
	log(fmt.Sprintf("localStorage captured: %d key(s)", len(storage)))

	d.sleep(d.clickMs)
	clickAria(tabCtx, "Start Chatting") // best effort: warms the session
	return store.Account{UserID: uid, Email: email, HashedToken: token, HasSession: true}, storage, true
}

// applyUAOverride forces the page's UA identity (header + navigator.userAgent)
// to the account's assigned UA — unique-UA mode. Best effort.
func applyUAOverride(ctx context.Context, ua string) {
	if ua == "" {
		return
	}
	_ = runTO(ctx, 5*time.Second, emulation.SetUserAgentOverride(ua).
		WithAcceptLanguage("en-US,en;q=0.9").
		WithPlatform("Win32"))
}

// applyAccountSettings forces the standard account settings right after
// registration (server-persisted via /api/updateUser, same as the app does):
// Privacy Mode ON, SFX ON, Video Auto-Roll OFF, Text Auto-Roll ON,
// Low Power Mode OFF, Disable Face Popup OFF — and the start-page MOOD is
// flipped to TEXT (local settings key + the real toggle click + verify).
func applyAccountSettings(ctx context.Context, log func(string)) {
	var upd string
	_ = runTO(ctx, 15*time.Second, chromedp.Evaluate(
		`(() => {
			const u = JSON.parse(localStorage.getItem('user') || '{}');
			if (!u.settings) return 'no-settings';
			u.settings.autoRoll = {video: false, text: true};
			u.settings.privacy = Object.assign({}, u.settings.privacy, {hidden: true});
			u.settings.volume = Object.assign({}, u.settings.volume, {master: 100, output: 100});
			u.settings.performance = Object.assign({}, u.settings.performance, {lowPower: false, facePopup: false});
			localStorage.setItem('user', JSON.stringify(u));
			return fetch('/api/updateUser', {method: 'PUT', headers: {'content-type': 'application/json'}, body: JSON.stringify({user: u})})
				.then(r => '' + r.status).catch(e => 'fetch:' + e);
		})()`,
		&upd, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	log("account settings applied (updateUser: " + upd + ")")

	// verify what the server actually stored
	var check string
	_ = runTO(ctx, 10*time.Second, chromedp.Evaluate(
		`(() => { const s = (JSON.parse(localStorage.getItem('user')||'{}').settings)||{};
			return 'privacy.hidden='+!!(s.privacy&&s.privacy.hidden)
				+' sfx(master)='+(s.volume&&s.volume.master)
				+' autoRoll.video='+(s.autoRoll&&s.autoRoll.video)
				+' autoRoll.text='+(s.autoRoll&&s.autoRoll.text)
				+' lowPower='+(s.performance&&s.performance.lowPower)
				+' facePopup='+(s.performance&&s.performance.facePopup); })()`,
		&check))
	log("settings verify: " + check)

	// The start page keeps its OWN client-side mood state in a separate
	// 'settings' localStorage key (NOT the user object) — accounts are born
	// with autoRoll {video:true, text:false} there, so the first page opens
	// in VIDEO mood even though the server object says text. Mirror the fix.
	var mood string
	_ = runTO(ctx, 10*time.Second, chromedp.Evaluate(
		`(() => {
			let s = {};
			try { s = JSON.parse(localStorage.getItem('settings') || '{}'); } catch(e) { s = {}; }
			s.autoRoll = {video: false, text: true};
			s.privacy = Object.assign({}, s.privacy, {hidden: true});
			localStorage.setItem('settings', JSON.stringify(s));
			return 'autoRoll=' + JSON.stringify(s.autoRoll);
		})()`,
		&mood))
	log("start-page mood key fixed (local settings: " + mood + ")")

	// Then do it the way a human does: open the start page and click the
	// real "Switch to Text Mode" toggle (right of the interests input), so
	// whatever storage the app uses for the mood is flipped through the UI.
	if err := runTO(ctx, 15*time.Second, chromedp.Navigate("https://thundr.com")); err != nil {
		log("start page navigate failed: " + err.Error())
		return
	}
	time.Sleep(3 * time.Second)
	if err := runTO(ctx, 6*time.Second, chromedp.Click(`button[aria-label="Switch to Text Mode"]`, chromedp.ByQuery)); err != nil {
		if !clickAria(ctx, "Switch to Text Mode") {
			log("mood toggle not found (may already be text mode)")
			return
		}
	}
	time.Sleep(800 * time.Millisecond)
	// The toggle does NOT flip its aria-label — the ACTIVE mode is the button
	// carrying the btn-primary class (Text = text mood selected).
	var confirm string
	_ = eval(ctx, `(() => {
		const t = document.querySelector('button[aria-label="Switch to Text Mode"]');
		const v = document.querySelector('button[aria-label="Switch to Video Mode"]');
		if (!t || !v) return 'toggle-not-found';
		const textActive = (t.className||'').includes('btn-primary');
		const videoActive = (v.className||'').includes('btn-primary');
		let s = {};
		try { s = JSON.parse(localStorage.getItem('settings')||'{}'); } catch(e) {}
		return 'TEXT-MODE-CONFIRMED text-btn=' + textActive + ' video-btn=' + videoActive + ' localAutoRoll=' + JSON.stringify(s.autoRoll||null);
	})()`, &confirm)
	log("mood verify: " + confirm)
}

// ---- visible bot (port of browser_bot.py) ---------------------------------------

type wsFrame struct {
	joinRef string
	ref     string
	topic   string
	event   string
	payload map[string]any
}

func parseFrame(raw string) (wsFrame, bool) {
	var arr []any
	if json.Unmarshal([]byte(raw), &arr) != nil || len(arr) < 4 {
		return wsFrame{}, false
	}
	f := wsFrame{payload: map[string]any{}}
	f.joinRef, _ = arr[0].(string)
	f.ref, _ = arr[1].(string)
	f.topic, _ = arr[2].(string)
	f.event, _ = arr[3].(string)
	if len(arr) > 4 {
		f.payload, _ = arr[4].(map[string]any)
	}
	return f, true
}

// RunVisibleBot runs one account in a visible Chrome window (blocking),
// reusing the account's persistent profile (cookies + storage = returning
// user, exactly like your manual browser session). attachURL selects the
// worker's chrome debug endpoint (multi-browser: one per account); when
// empty it falls back to the settings' chrome_attach_url.
func RunVisibleBot(ctx context.Context, st *store.Store, acc store.Account, proxy string, cb Callbacks, attachURL string) error {
	// Profile dir OR a saved session dump is enough: the session can be
	// injected into a fresh profile (reference UseSavedSession behaviour).
	if !st.HasProfile(acc.UserID) && len(SavedSessionEntries(st, acc.UserID)) == 0 {
		return fmt.Errorf("no browser profile or saved session for %s (register first)", acc.UserID[:8])
	}
	if attachURL == "" {
		attachURL = st.Get("chrome_attach_url", "")
	}
	if attachURL != "" && !EnsureBotChromeOn(st, acc.UserID, attachURL, func(m string) { cb.Log(acc.UserID, "setup", "["+acc.UserID[:8]+"] "+m) }) {
		return fmt.Errorf("browser not reachable on debug port")
	}
	d := loadDelays(st)
	mgr := reply.NewManager(st)
	interests := st.LoadList("interests")
	respMin, respMax := st.RangeSecs("stranger_response_time", 2, 5) // reply delay range (random per reply)
	workT := st.Float("thread_work_time", 600)
	noRespMin, noRespMax := st.RangeSecs("partner_no_response_secs", 10, 15) // silent-skip range (random per match)
	noRespCur := store.RandBetween(noRespMin, noRespMax)
	instSkip := st.Float("instant_skip_secs", 5)
	noMatchT := st.Float("search_no_match_secs", 75)                 // search watchdog: reload queue after this long with no match
	rerollMin, rerollMax := st.RangeSecs("reroll_delay_secs", 6, 12) // re-roll pause range (random per re-roll)
	sendsFirst := st.Bool("bot_sends_first", false)                  // true = send the opener ourselves after a match
	tag := acc.UserID[:8]
	say := func(state, msg string) { cb.Log(acc.UserID, state, "["+tag+"] "+msg) }

	browserCtx, cancel := newVisibleChromeOn(ctx, proxy, st.ProfileDir(acc.UserID), attachURL)
	defer cancel()

	if err := chromedp.Run(browserCtx, addInitScript(initJS)); err != nil {
		return fmt.Errorf("chrome launch failed: %w", err)
	}
	// unique-UA mode: make the JS-visible identity match the account UA too.
	if st.Bool("unique_ua_per_browser", false) {
		applyUAOverride(browserCtx, st.UserAgentFor(acc.UserID))
	}
	if proxy != "" {
		bindProxyLocale(browserCtx, proxy, func(m string) { say("setup", m) })
	}

	if mgr.Warning != "" {
		say("setup", "message: "+mgr.Warning)
	}
	if !mgr.Ready() {
		return fmt.Errorf("no chat lines configured (Message panel)")
	}
	say("starting", fmt.Sprintf("visible bot starting - chat mode: %s", mgr.Settings.ChatMode))
	cb.Status(acc.UserID, 0, 0, 0)

	if err := chromedp.Run(browserCtx, chromedp.Navigate("https://thundr.com")); err != nil {
		return err
	}
	time.Sleep(5 * time.Second)
	// reference-style ban check: suspended accounts stop loudly, not loop.
	if reason, banned := CheckBanned(browserCtx); banned {
		return BannedError{Reason: reason}
	}
	var loggedIn bool
	_ = eval(browserCtx, `(() => { try { return !!JSON.parse(localStorage.getItem('user')||'{}').hashed_token } catch(e){ return false } })()`, &loggedIn)
	if !loggedIn {
		// the profile's cookies were rejected/expired — replay the saved
		// localStorage instead of failing into a manual login.
		say("setup", "profile not logged in - trying saved-session injection")
		if !RestoreSession(browserCtx, st, acc.UserID, func(m string) { say("setup", m) }) {
			return fmt.Errorf("not logged in (profile rejected and session injection failed)")
		}
	}
	say("setup", "logged in OK")
	TouchProfile(st, acc.UserID) // profile record: last_used_at

	// interests box (human typing)
	boxSel := `input[placeholder="Your interests, TikTok, Gaming etc..."]`
	if len(interests) > 0 && exists(browserCtx, boxSel) {
		for _, t := range interests {
			say("setup", "typing interest: "+t)
			if err := runTO(browserCtx, 5*time.Second, chromedp.Click(boxSel, chromedp.ByQuery)); err != nil {
				say("setup", "interests box not clickable: "+err.Error())
				break
			}
			d.sleep(80 + rand.Intn(100))
			for _, r := range t {
				_ = runTO(browserCtx, 3*time.Second, chromedp.SendKeys(boxSel, string(r), chromedp.ByQuery))
				time.Sleep(d.typeDelay())
			}
			d.sleep(150 + rand.Intn(250))
			_ = runTO(browserCtx, 3*time.Second, chromedp.SendKeys(boxSel, "\r", chromedp.ByQuery))
			time.Sleep(400 * time.Millisecond)
		}
		say("setup", "interests set: "+strings.Join(interests, ", "))
	}

	// trusted CDP mouse clicks (JS .click() is ignored by the app here)
	d.sleep(d.clickMs)
	if err := runTO(browserCtx, 6*time.Second,
		chromedp.Click(`button[aria-label="Switch to Text Mode"]`, chromedp.ByQuery)); err != nil {
		clickAria(browserCtx, "Switch to Text Mode")
	}
	say("setup", "text mode selected")
	time.Sleep(800 * time.Millisecond)

	d.sleep(d.clickMs)
	if err := runTO(browserCtx, 6*time.Second,
		chromedp.Click(`button[aria-label="Start Chatting"]`, chromedp.ByQuery)); err != nil {
		clickAria(browserCtx, "Start Chatting")
	}
	say("searching", "clicked Start (app handles captcha)")

	// one-time diagnostic 8s later: is the app really logged in + searching?
	go func() {
		time.Sleep(8 * time.Second)
		var diag map[string]any
		_ = eval(browserCtx, `({
			href: location.href,
			ws_open: !!(window.__ws && window.__ws.readyState <= 1),
			frames: (window.__frames || []).length,
			hcaptcha: !!document.querySelector('iframe[src*="hcaptcha"]')
		})`, &diag)
		b, _ := json.Marshal(diag)
		say("diagnostic", string(b))
	}()

	// deeper overlay dump at 15s: what's covering the chat UI?
	go func() {
		time.Sleep(15 * time.Second)
		var dump map[string]any
		_ = eval(browserCtx, `(() => {
			// find visible elements that cover most of the viewport
			const vw = innerWidth, vh = innerHeight;
			const covers = [];
			document.querySelectorAll('body *').forEach(el => {
				const r = el.getBoundingClientRect();
				if (r.width > vw*0.6 && r.height > vh*0.5) {
					const cs = getComputedStyle(el);
					if (cs.position === 'fixed' || cs.position === 'absolute') {
						covers.push({
							tag: el.tagName,
							cls: (el.className||'').toString().slice(0,120),
							id: el.id,
							z: cs.zIndex,
							text: (el.innerText||'').slice(0,100),
							btns: [...el.querySelectorAll('button')].map(b => (b.getAttribute('aria-label')||(b.textContent||'').trim()).slice(0,30)).slice(0,8),
							iframes: [...el.querySelectorAll('iframe')].map(f => (f.src||'').slice(0,80)).slice(0,4)
						});
					}
				}
			});
			return {
				buttons: [...document.querySelectorAll('button')].map(b => (b.getAttribute('aria-label')||(b.textContent||'').trim()).slice(0,30)).slice(0,25),
				covers: covers.slice(0,6)
			};
		})()`, &dump)
		if raw, err := json.Marshal(dump); err == nil {
			s := string(raw)
			if len(s) > 900 {
				s = s[:900]
			}
			say("overlay-dump", s)
		}
	}()

	var (
		ref             = 1000
		joinRefs        = map[string]string{}
		roomTopic       string
		matchID         string
		partnerID       string
		sess            reply.Session
		matches         int
		replies         int
		matchTopic      = "match:" + acc.UserID
		started         = time.Now()
		lastSearch      = time.Now()
		matchStart      time.Time
		lastActivity    time.Time
		pauseUntil      time.Time
		searchStartedAt = time.Now() // actual Start-click time (for true queue-wait measurement)
		socketMisses    int          // consecutive dead-socket reloads (backoff under throttle)
		intel           *bot.MatchIntel
		bannedKw        = []string{"ban", "suspend", "blocked", "kicked"}
	)

	// forward-declared: assigned below, used by skipPartner and the loop
	var sendFrame func(topic, event string, payload map[string]any)
	var endIntel func(reason string)

	skipPartner := func(reason string) {
		say("searching", reason+" - skipping partner")
		// skip THROUGH THE SITE UI (its own Skip/Next button) so the action is
		// visible in the window AND processed by the app itself; raw ws frames
		// stay as the fallback when the button cannot be found.
		if !clickSiteButton(browserCtx, `^\s*(skip|next)\s*$`) {
			sendFrame(roomTopic, "phx_leave", map[string]any{})
			sendFrame(matchTopic, "disconnect", map[string]any{
				"to": partnerID, "from": acc.UserID, "match_id": matchID,
				"mode": "text", "videoLoaded": false})
		}
		// the site's Skip auto-requeues us — CANCEL that too, or the reroll
		// pause below gets bypassed by an instant server-side re-match.
		time.Sleep(800 * time.Millisecond) // let the search dialog render
		clickSiteButton(browserCtx, `^\s*cancel\s*$`)
		endIntel(reason)
		roomTopic, matchID, partnerID = "", "", ""
		sess = nil
		// re-search after the configured reroll pause (random inside the range)
		pauseUntil = time.Now().Add(time.Duration(store.RandBetween(rerollMin, rerollMax)) * time.Second)
		lastSearch = time.Now().Add(-21 * time.Second) // click-eligible as soon as the pause ends
	}

	endIntel = func(reason string) {
		if intel == nil {
			return
		}
		cls, why := intel.Classify()
		if cb.MatchEnd != nil {
			cb.MatchEnd(acc.UserID, cls, why)
		}
		say("", fmt.Sprintf("match ended (%s) -> partner classified %s %v", reason, cls, why))
		intel = nil
	}
	nextRef := func() string { ref++; return fmt.Sprint(ref) }

	sendFrame = func(topic, event string, payload map[string]any) {
		jr, ok := joinRefs[topic]
		r := nextRef()
		if event == "phx_join" || !ok {
			jr = r
		}
		frame, _ := json.Marshal([]any{jr, r, topic, event, payload})
		var sent bool
		_ = eval(browserCtx, `window.__send(`+strconv.Quote(string(frame))+`)`, &sent)
	}

	// sendChatMessage delivers one chat line THROUGH THE SITE UI (type into the
	// message box + click Send with a real mouse click) so the bot's own
	// messages render as bubbles in the browser window — exactly like a human.
	// Falls back to the raw ws frame when the UI path is unavailable.
	sendChatMessage := func(text string) {
		uiOK := func() bool {
			// 1) find the chat input + Send button
			var found string
			if err := runTO(browserCtx, 3*time.Second, chromedp.Evaluate(`(() => {
				const inputs = [...document.querySelectorAll('input[type="text"], input:not([type]), textarea, [contenteditable="true"]')]
					.filter(el => el.offsetParent !== null && !el.disabled);
				if (!inputs.length) return 'noinput';
				const inp = inputs.find(el => /message|chat|say/i.test(el.placeholder || el.getAttribute('aria-label') || '')) || inputs[0];
				const btns = [...document.querySelectorAll('button, [role="button"]')]
					.filter(b => b.offsetParent !== null);
				const send = btns.find(b => /^send$/i.test((b.innerText || '').trim()) || /^send$/i.test(b.getAttribute('aria-label') || ''));
				if (!send) return 'nosend';
				inp.scrollIntoView({block: 'center'});
				inp.focus();
				const r = send.getBoundingClientRect();
				return JSON.stringify({x: r.x + r.width / 2, y: r.y + r.height / 2});
			})()`, &found)); err != nil || found == "" || found == "noinput" || found == "nosend" {
				return false
			}
			// 2) type the text with real input events (React picks it up)
			if err := runTO(browserCtx, 5*time.Second, chromedp.ActionFunc(func(ctx context.Context) error {
				return input.InsertText(text).Do(ctx)
			})); err != nil {
				return false
			}
			// 3) real mouse click on Send (this site's React ignores .click())
			var pt struct {
				X, Y float64
			}
			if json.Unmarshal([]byte(found), &pt) != nil {
				return false
			}
			time.Sleep(150 * time.Millisecond)
			if err := runTO(browserCtx, 3*time.Second, chromedp.MouseClickXY(pt.X, pt.Y)); err != nil {
				return false
			}
			// 4) sent = input got cleared by the app
			time.Sleep(350 * time.Millisecond)
			var leftover string
			_ = runTO(browserCtx, 2*time.Second, chromedp.Evaluate(`(() => {
				const inputs = [...document.querySelectorAll('input[type="text"], input:not([type]), textarea, [contenteditable="true"]')]
					.filter(el => el.offsetParent !== null && !el.disabled);
				const inp = inputs.find(el => /message|chat|say/i.test(el.placeholder || el.getAttribute('aria-label') || '')) || inputs[0];
				if (!inp) return '';
				return inp.isContentEditable ? (inp.innerText || '') : (inp.value || '');
			})()`, &leftover))
			if strings.TrimSpace(leftover) != "" {
				// not sent - wipe the draft so the next reply starts clean
				_ = runTO(browserCtx, 2*time.Second, chromedp.Evaluate(`(() => {
					const inputs = [...document.querySelectorAll('input[type="text"], input:not([type]), textarea, [contenteditable="true"]')];
					const inp = inputs.find(el => /message|chat|say/i.test(el.placeholder || el.getAttribute('aria-label') || '')) || inputs[0];
					if (!inp) return;
					if (inp.isContentEditable) { inp.innerText = ''; inp.dispatchEvent(new Event('input', {bubbles: true})); }
					else { const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set; s.call(inp, ''); inp.dispatchEvent(new Event('input', {bubbles: true})); }
				})()`, nil))
				return false
			}
			return true
		}()
		if !uiOK {
			sendFrame(roomTopic, "message", map[string]any{"body": text, "sender_id": acc.UserID})
		}
	}

	lastModalCheck := time.Now()
	lastBeat := time.Now()
	for {
		select {
		case <-ctx.Done():
			say("finished", "stopped")
			return nil
		default:
		}
		if time.Since(started) > time.Duration(workT*float64(time.Second)) {
			say("finished", "work time over")
			return nil
		}

		// captcha dialog + modal handling every ~4s
		if time.Since(lastModalCheck) > 4*time.Second {
			lastModalCheck = time.Now()

			// 1) hCaptcha challenge dialog: <dialog class="modal"> with Cancel/Skip
			var cap struct {
				Open   bool     `json:"open"`
				Iframe bool     `json:"iframe"`
				X      float64  `json:"x"`
				Y      float64  `json:"y"`
				Btns   []string `json:"btns"`
			}
			_ = eval(browserCtx, `(() => {
				const ds=[...document.querySelectorAll('dialog.modal')];
				const d=ds.find(x=>(x.innerText||'').trim().length>0) || ds.find(x=>x.open);
				if(!d) return {open:false};
				const btns=[...d.querySelectorAll('button')].map(b=>(b.textContent||'').trim());
				const f=d.querySelector('iframe[src*="hcaptcha"]');
				if(!f) return {open:true, iframe:false, btns};
				const fr=f.getBoundingClientRect();
				return {open:true, iframe:true, x:fr.left, y:fr.top, btns};
			})()`, &cap)

			if cap.Open {
				if cap.Iframe {
					// checkbox sits ~30px in from the iframe's left edge
					cx := cap.X + 30
					cy := cap.Y + 40
					say("captcha", fmt.Sprintf("hCaptcha widget found - clicking checkbox at %.0f,%.0f", cx, cy))
					humanMouse(browserCtx)
					_ = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
						if err := input.DispatchMouseEvent(input.MousePressed, cx, cy).
							WithButton("left").WithClickCount(1).Do(ctx); err != nil {
							return err
						}
						time.Sleep(60 * time.Millisecond)
						return input.DispatchMouseEvent(input.MouseReleased, cx, cy).
							WithButton("left").WithClickCount(1).Do(ctx)
					}))
					time.Sleep(3 * time.Second)
				} else {
					// no hcaptcha widget: maybe a stray confirm dialog ("Skip this user?")
					var dlgText string
					_ = eval(browserCtx, `(() => {
						const ds=[...document.querySelectorAll('dialog.modal')];
						const d=ds.find(x=>(x.innerText||'').trim().length>0) || ds.find(x=>x.open);
						return d ? (d.innerText||d.textContent||'').slice(0,120) : '';
					})()`, &dlgText)
					if strings.Contains(strings.ToLower(dlgText), "skip this user") {
						say("modal", "dismissing stray skip-confirm dialog (real click)")
						var rect struct {
							X, Y, W, H float64
						}
						_ = eval(browserCtx, `(() => {
							const ds=[...document.querySelectorAll('dialog.modal')];
							const d=ds.find(x=>(x.innerText||'').trim().length>0) || ds.find(x=>x.open);
							if(!d) return null;
							const b=[...d.querySelectorAll('button')].find(x => /cancel/i.test((x.textContent||'').trim()));
							if(!b) return null;
							const r=b.getBoundingClientRect();
							return {x:r.left, y:r.top, w:r.width, h:r.height};
						})()`, &rect)
						if rect.W > 0 {
							cx := rect.X + rect.W/2
							cy := rect.Y + rect.H/2
							humanMouse(browserCtx)
							_ = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
								if err := input.DispatchMouseEvent(input.MousePressed, cx, cy).
									WithButton("left").WithClickCount(1).Do(ctx); err != nil {
									return err
								}
								time.Sleep(60 * time.Millisecond)
								return input.DispatchMouseEvent(input.MouseReleased, cx, cy).
									WithButton("left").WithClickCount(1).Do(ctx)
							}))
						}
						time.Sleep(800 * time.Millisecond)
					} else {
						say("captcha", "challenge dialog open, widget not rendered yet (passive solve pending)")
					}
				}
			} else {
				// 2) generic modal dismissal (coin popups etc.)
				var modalText string
				_ = eval(browserCtx, `(() => { const m=document.querySelector('[role="dialog"]'); if(!m) return ''; const r=m.getBoundingClientRect(); if(r.width<50||r.height<50) return ''; return (m.innerText||'').slice(0,150) })()`, &modalText)
				if modalText != "" {
					say("modal", "dismissing modal: "+strings.ReplaceAll(modalText, "\n", " "))
					var closed bool
					_ = eval(browserCtx, `(() => {
						const m=document.querySelector('[role="dialog"]');
						if(!m) return false;
						const btns=[...m.querySelectorAll('button')];
						const close=btns.find(b => /close|no thanks|not now|later|dismiss|×|✕|✖/i.test((b.getAttribute('aria-label')||'')+' '+(b.textContent||'')));
						if(close){close.click();return true}
						const x=m.querySelector('[class*="close" i], [aria-label*="close" i]');
						if(x){x.click();return true}
						return false;
					})()`, &closed)
					if !closed {
						_ = chromedp.Run(browserCtx, chromedp.KeyEvent("\x1b")) // Escape
					}
					time.Sleep(800 * time.Millisecond)
				}
			}
		}

		// heartbeat: show search health every 15s
		if time.Since(lastBeat) > 15*time.Second {
			lastBeat = time.Now()
			var hb struct {
				Href    string `json:"href"`
				WsOpen  bool   `json:"ws"`
				Start   bool   `json:"start"`
				Stop    bool   `json:"stop"`
				Next    bool   `json:"next"`
				ModalTx string `json:"modal"`
			}
			_ = eval(browserCtx, `({
				href: location.pathname,
				ws: !!(window.__ws && window.__ws.readyState === 1),
				start: !!document.querySelector('button[aria-label="Start Chatting"]') || [...document.querySelectorAll('button')].some(b => /^start\b/i.test((b.innerText || '').trim())),
				stop: !!document.querySelector('button[aria-label="Stop Chatting"]'),
				next: !!document.querySelector('button[aria-label="Next"]'),
				modal: (document.querySelector('[role="dialog"]')||{}).innerText || ''
			})`, &hb)
			say("searching", fmt.Sprintf("hb: page=%s ws=%v startBtn=%v stopBtn=%v nextBtn=%v room=%v modal=%.60q",
				hb.Href, hb.WsOpen, hb.Start, hb.Stop, hb.Next, roomTopic != "", hb.ModalTx))
		}

		var frames []string
		_ = eval(browserCtx, `window.__drain()`, &frames)
		for _, raw := range frames {
			fr, ok := parseFrame(raw)
			if !ok {
				continue
			}
			// ban detection: only SERVER-side close/error/disconnect events on OUR
			// match/room topics count — scanning every frame for "ban" false-
			// positives on ordinary words like "urban".
			isBanEvent := (fr.event == "phx_close" || fr.event == "phx_error" || fr.event == "disconnect") &&
				(fr.topic == matchTopic || fr.topic == roomTopic)
			if isBanEvent {
				low := strings.ToLower(raw)
				for _, kw := range bannedKw {
					if strings.Contains(low, kw) {
						say("banned", "server ban signal ("+fr.event+") - account will be removed")
						return BannedError{Reason: "server ban signal (" + fr.event + ")"}
					}
				}
			}
			switch fr.event {
			case "phx_join":
				joinRefs[fr.topic] = fr.joinRef
			case "matched":
				if time.Now().Before(pauseUntil) {
					// thundr auto-requeues server-side on partner-disconnect — the
					// match is already made when we see it. Honor reroll_delay:
					// skip it + leave the queue; the Start click comes after the pause.
					say("searching", "match arrived during reroll pause - skipping (reroll_delay_secs)")
					clickSiteButton(browserCtx, `^\s*(skip|next)\s*$`)
					time.Sleep(800 * time.Millisecond)
					clickSiteButton(browserCtx, `^\s*cancel\s*$`)
					continue
				}
				mid, _ := fr.payload["match_id"].(string)
				partner, _ := fr.payload["partner"].(map[string]any)
				partnerID, _ = partner["id"].(string)
				raw, _ := json.Marshal(fr.payload)
				shared, _ := fr.payload["shared_interests"].([]any)
				roomTopic = "room:" + mid
				matchID = mid
				sess = mgr.NewSession()
				matches++
				matchStart = time.Now()
				lastActivity = time.Now()
				noRespCur = store.RandBetween(noRespMin, noRespMax) // fresh random threshold per match
				intel = &bot.MatchIntel{
					MatchedAt: matchStart, SearchSecs: time.Since(searchStartedAt).Seconds(),
					SharedInterests: len(shared), PartnerProfileRaw: string(raw),
				}
				cb.Status(acc.UserID, matches, replies, 0)
				say("matched", fmt.Sprintf("MATCHED %.8s after %.1fs shared=%v", mid, intel.SearchSecs, fr.payload["shared_interests"]))
				say("", fmt.Sprintf("matched payload: %.300s", string(raw)))
				// opener (optional): when bot_sends_first is on, the bot's first line
				// goes out through the site UI right after the match settles.
				if sendsFirst && sess != nil {
					if opener := sess.Open(); strings.TrimSpace(opener) != "" {
						go func(text string) {
							time.Sleep(time.Duration(store.RandBetween(respMin, respMax)*1000) * time.Millisecond)
							if roomTopic == "" {
								return // partner left before we opened
							}
							sendFrame(roomTopic, "typing", map[string]any{"sender_id": acc.UserID})
							typingMs := 300 + len(text)*75 + rand.Intn(400)
							if typingMs > 1200 {
								typingMs = 1200 // openers type fast
							}
							time.Sleep(time.Duration(typingMs) * time.Millisecond)
							if roomTopic == "" {
								return
							}
							for _, part := range strings.Split(text, "\n") {
								if part = strings.TrimSpace(part); part != "" {
									sendChatMessage(part)
								}
							}
							if intel != nil && intel.OurFirstReplyAt.IsZero() {
								intel.OurFirstReplyAt = time.Now()
							}
						}(opener)
					}
				}
			case "message":
				if fr.topic == roomTopic {
					if sid, _ := fr.payload["sender_id"].(string); sid != acc.UserID {
						body, _ := fr.payload["body"].(string)
						say("matched", "PARTNER: "+body)
						lastActivity = time.Now()
						if intel != nil {
							intel.PartnerMsgCount++
							if intel.FirstPartnerMsgAt.IsZero() {
								intel.FirstPartnerMsgAt = time.Now()
								intel.FirstPartnerMsg = body
							}
						}
						var line string
						done := sess == nil
						sent, total := replies, 0
						if sess != nil {
							line, done = sess.Next(body)
							sent, total = sess.Progress()
						}
						if done || line == "" {
							say("searching", "chat exhausted - next match")
							endIntel("script exhausted")
							if !clickSiteButton(browserCtx, `^\s*(skip|next)\s*$`) {
								sendFrame(roomTopic, "phx_leave", map[string]any{})
								sendFrame(matchTopic, "disconnect", map[string]any{
									"to": sid, "from": acc.UserID, "match_id": matchID,
									"mode": "text", "videoLoaded": false})
							}
							// Skip auto-requeues: cancel so the reroll pause holds
							time.Sleep(800 * time.Millisecond)
							clickSiteButton(browserCtx, `^\s*cancel\s*$`)
							roomTopic, matchID = "", ""
							sess = nil
							pauseUntil = time.Now().Add(time.Duration(store.RandBetween(rerollMin, rerollMax)) * time.Second)
							lastSearch = time.Now().Add(-21 * time.Second) // click-eligible right after the pause (was a hidden 20s)
						} else {
							// Fast opener: the FIRST reply goes out quickly (people
							// bail in <2s otherwise); later replies draw a random
							// delay inside the configured min-max range.
							if sent == 0 {
								openerMax := respMin
								if openerMax < 0.8 {
									openerMax = 0.8
								}
								time.Sleep(time.Duration(store.RandBetween(0.3, openerMax)*1000) * time.Millisecond)
							} else {
								time.Sleep(time.Duration(store.RandBetween(respMin, respMax)*1000) * time.Millisecond)
							}
							sendFrame(roomTopic, "typing", map[string]any{"sender_id": acc.UserID})
							typingMs := 300 + len(line)*75 + rand.Intn(400)
							if sent == 0 {
								// opener: short messages type fast - don't stall
								if typingMs > 1200 {
									typingMs = 1200
								}
							} else if typingMs > 3500 {
								typingMs = 3500
							}
							time.Sleep(time.Duration(typingMs) * time.Millisecond)
							if roomTopic == "" { // partner left while typing
								continue
							}
							// API Chat may return two lines (lead-in + share): send separately
							for _, part := range strings.Split(line, "\n") {
								if part = strings.TrimSpace(part); part != "" {
									sendChatMessage(part) // UI-first so messages render in the window
								}
							}
							lastActivity = time.Now()
							if intel != nil && intel.OurFirstReplyAt.IsZero() {
								intel.OurFirstReplyAt = time.Now()
							}
							replies++
							cb.Status(acc.UserID, matches, replies, sent)
							if total > 0 && sent == total && cb.Share != nil {
								cb.Share(acc.UserID, mgr.Settings.SharePlatform) // final line = share line
							}
							if total > 0 {
								say("matched", fmt.Sprintf("REPLIED (%d/%d): %.80s", sent, total, line))
							} else {
								say("matched", fmt.Sprintf("REPLIED (%d): %.80s", sent, line))
							}
						}
					}
				}
			case "disconnect":
				if fr.topic == matchTopic && matchID != "" {
					dur := time.Since(matchStart)
					say("searching", fmt.Sprintf("partner disconnected (after %.1fs)", dur.Seconds()))
					if intel != nil {
						intel.PartnerLeftAt = time.Now()
					}
					endIntel("partner left")
					roomTopic, matchID, partnerID = "", "", ""
					sess = nil
					if dur < time.Duration(instSkip*float64(time.Second)) {
						// instant-skipper: re-roll immediately, no pause
						say("searching", fmt.Sprintf("partner skipped <%.0fs - starting next match now", instSkip))
						pauseUntil = time.Now().Add(time.Second)
						lastSearch = time.Now().Add(-21 * time.Second)
					} else {
						// CANCEL the search first: thundr auto-requeues us on
						// partner-disconnect, which would bypass the reroll pause
						// (a fresh match can arrive within the same second).
						clickSiteButton(browserCtx, `^\s*cancel\s*$`)
						// humans pause before re-rolling (reroll_delay_secs range, random each time)
						pauseUntil = time.Now().Add(time.Duration(store.RandBetween(rerollMin, rerollMax)) * time.Second)
						lastSearch = time.Now().Add(-21 * time.Second) // click-eligible right after the pause (was a hidden 20s)
					}
				}
			}
		}

		// auto-skip: partner silent while matched
		if roomTopic != "" && time.Since(lastActivity) > time.Duration(noRespCur*float64(time.Second)) {
			skipPartner(fmt.Sprintf("partner no response in %ds", int(noRespCur)))
		}

		if roomTopic == "" && time.Now().After(pauseUntil) && time.Since(lastSearch) > 20*time.Second {
			lastSearch = time.Now()
			// click Start wherever it renders (home page OR idle chat page's
			// "Start Esc" variant) — the aria-label alone misses the chat-page one
			if clickStartButton(browserCtx) {
				say("searching", "clicked Start - new search")
				searchStartedAt = time.Now() // real queue wait starts at the actual click
			} else {
				// no Start button: app may already be searching, or socket died
				if searchStartedAt.IsZero() {
					searchStartedAt = time.Now() // we never clicked: start measuring now
				}
				var wsOpen bool
				_ = eval(browserCtx, `!!(window.__ws && window.__ws.readyState === 1)`, &wsOpen)
				if !wsOpen {
					socketMisses++
					// under IP throttle hammering reloads makes it worse - back off
					backoff := time.Duration(5+socketMisses*10) * time.Second
					if backoff > 90*time.Second {
						backoff = 90 * time.Second
					}
					if socketMisses >= 3 {
						// plain Reload can leave the tab wedged (page="") under
						// throttle - force a FRESH navigation instead
						say("searching", fmt.Sprintf("socket lost x%d - fresh navigate to thundr", socketMisses))
						_ = runTO(browserCtx, 25*time.Second, chromedp.Navigate(startPage))
					} else {
						say("searching", fmt.Sprintf("socket closed - reloading page (backoff %ds)", int(backoff.Seconds())))
						_ = runTO(browserCtx, 20*time.Second, chromedp.Reload())
					}
					time.Sleep(backoff)
				} else {
					socketMisses = 0 // socket healthy again
					if time.Since(searchStartedAt) > time.Duration(noMatchT*float64(time.Second)) {
						// search watchdog: queued too long with no match (server-side
						// starvation/throttle) - reload for a fresh queue slot.
						say("searching", fmt.Sprintf("no match in %ds - reloading for a fresh queue slot", int(noMatchT)))
						_ = runTO(browserCtx, 20*time.Second, chromedp.Reload())
						time.Sleep(5 * time.Second)
						searchStartedAt = time.Now()
						lastSearch = time.Now().Add(-21 * time.Second) // force re-click next pass
					}
				}
			}
		}
		time.Sleep(600 * time.Millisecond)
	}
}
