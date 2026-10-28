package browser

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"thundr/internal/store"
)

// ---- dashboard-driven chrome launch (SINGLE path) -----------------------------
//
// There is exactly ONE way a browser is opened: the exe spawns the installed
// chrome.exe itself â€” a real, visible window on the desktop â€” then attaches
// over the DevTools port. No scheduled task, no request-file IPC, no .cmd
// launcher: the old dual backend (watcher first, direct spawn as fallback)
// had two ways to disagree, so only the direct spawn is left. The spawn is
// detached so chrome outlives the dashboard process.
// MULTI-BROWSER: each account chrome gets its own debug port (register = 9300,
// manual = 9301, workers = 9222+i assigned by dash).

// chromeExe finds the installed chrome binary.
func chromeExe() string {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("LocalAppData"), `Google\Chrome\Application\chrome.exe`),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return `C:\Program Files\Google\Chrome\Application\chrome.exe`
}

// winDir is the Windows directory with a sane fallback.
func winDir() string {
	if r := os.Getenv("SystemRoot"); r != "" && !strings.HasPrefix(r, "/") {
		return r
	}
	if r := os.Getenv("windir"); r != "" && !strings.HasPrefix(r, "/") {
		return r
	}
	return `C:\Windows`
}

// winTemp is a real Windows temp directory (never a POSIX path).
func winTemp() string {
	for _, v := range []string{os.Getenv("TEMP"), os.Getenv("TMP")} {
		if v != "" && !strings.HasPrefix(v, "/") {
			return v
		}
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" || strings.HasPrefix(base, "/") {
		base = winDir()
	}
	return filepath.Join(base, "Temp")
}

// childEnv builds a clean environment for spawned chrome. A dashboard started
// from a POSIX shell / unusual wrapper inherits a PATH made of /usr/bin
// entries; chrome then fails with "The system cannot find the path specified"
// or DLL-init errors, which made the browser open button fail for no visible
// reason.
// "The system cannot find the path specified" or DLL-init errors, which made
// the browser open button fail for no visible reason.
func childEnv() []string {
	sys := filepath.Join(winDir(), "System32")
	env := []string{
		"SystemRoot=" + winDir(),
		"windir=" + winDir(),
		"PATH=" + strings.Join([]string{
			sys,
			winDir(),
			filepath.Join(sys, "Wbem"),
			filepath.Join(sys, "WindowsPowerShell", "v1.0"),
			filepath.Join(sys, "OpenSSH"),
		}, ";"),
		"COMSPEC=" + filepath.Join(sys, "cmd.exe"),
		"TEMP=" + winTemp(),
		"TMP=" + winTemp(),
	}
	for _, k := range []string{
		"USERPROFILE", "USERNAME", "USERDOMAIN", "APPDATA", "LOCALAPPDATA",
		"ProgramFiles", "ProgramFiles(x86)", "ProgramData", "HOMEDRIVE", "HOMEPATH",
		"COMPUTERNAME", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "LANG",
	} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// attachPortOf extracts the port from an attach URL (http://127.0.0.1:9222 -> 9222).
func attachPortOf(attachURL string) string {
	u := strings.TrimSuffix(attachURL, "/")
	if i := strings.LastIndex(u, ":"); i >= 0 && i+1 < len(u) {
		return u[i+1:]
	}
	return "9222"
}

func attachPort() string { return attachPortOf(AttachURL) }

// DebugPortUpOn reports whether a chrome DevTools endpoint answers on attachURL.
func DebugPortUpOn(attachURL string) bool {
	if strings.TrimSpace(attachURL) == "" {
		return false
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(strings.TrimSuffix(attachURL, "/") + "/json/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// DebugPortUp reports whether a chrome DevTools endpoint answers on AttachURL.
func DebugPortUp() bool { return DebugPortUpOn(AttachURL) }

// CloseChromeOn gracefully closes the chrome behind attachURL by sending
// Browser.close over the BROWSER-level DevTools endpoint (a page session
// cannot close the browser; the browser endpoint closes every window and the
// process â€” verified against guest-mode chrome too). Best effort: silently
// no-ops when nothing listens there. Used so the window count always matches
// the thread amount (register chrome closes when bots start; worker chromes
// close on Stop).
func CloseChromeOn(attachURL string) {
	if !DebugPortUpOn(attachURL) {
		return
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(attachURL, "/") + "/json/version")
	if err != nil {
		return
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if v.WebSocketDebuggerURL == "" {
		return
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(v.WebSocketDebuggerURL, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = conn.WriteJSON(map[string]any{"id": 1, "method": "Browser.close"})
	time.Sleep(300 * time.Millisecond) // let chrome process the close before the ws drops
}

// chromeArgs is the single source of truth for the flags every bot chrome gets.
// The window is ALWAYS visible: no --headless flag is ever passed (hCaptcha and
// the human-flow checks need a real window), and the automation leak is patched
// with --disable-blink-features=AutomationControlled.
func chromeArgs(profileDir, port, ua, url string, guest bool) []string {
	if url == "" {
		url = "about:blank"
	}
	args := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=" + port,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-blink-features=AutomationControlled",
	}
	if guest {
		args = append(args, "--guest")
	}
	if ua != "" {
		args = append(args, "--user-agent="+ua)
	}
	return append(args, url)
}

// rootOf returns the project root that owns data/ (data dir's parent).
func rootOf(st *store.Store) string {
	root := st.Dir
	if filepath.Base(root) == "data" {
		root = filepath.Dir(root)
	}
	return root
}

// waitPort polls a DevTools endpoint until it answers or the deadline passes.
func waitPort(attachURL string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if DebugPortUpOn(attachURL) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// ensureChromeOn makes sure a chrome with profileDir listens on attachURL's
// port. SINGLE PATH: the exe spawns the installed chrome itself — a visible
// window, detached so it outlives the dashboard. ua, when non-empty, overrides
// the user agent (unique-UA mode); url is chrome's start page (no blank tab);
// guest = fresh Guest session.
func ensureChromeOn(st *store.Store, profileDir, attachURL, ua, url string, guest bool, log func(string)) bool {
	if DebugPortUpOn(attachURL) {
		return true
	}
	port := attachPortOf(attachURL)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		log("profile dir create failed: " + err.Error())
	}
	log("opening chrome (profile " + filepath.Base(profileDir) + ", port " + port + ")")
	if err := spawnDetached(chromeExe(), chromeArgs(profileDir, port, ua, url, guest), profileDir, childEnv()); err != nil {
		log("chrome spawn failed: " + err.Error())
		return false
	}
	log("chrome spawned directly (detached)")
	if waitPort(attachURL, 15*time.Second) {
		log("browser up on debug port " + port)
		return true
	}
	log("chrome did not open within 15s")
	return false
}

// startPage is where every bot chrome lands directly (no blank tab).
const startPage = "https://thundr.com/"

// RegisterURL is the register chrome's DEDICATED debug endpoint â€” it must
// never share a port with worker chromes (worker 1 uses the base port), or
// opening a browser for registration would look like an extra worker window.
func RegisterURL(st *store.Store) string {
	return "http://127.0.0.1:9300"
}

// EnsureRegisterChrome guarantees the register chrome is reachable on its
// dedicated port. It uses a PERSISTENT profile (never --guest): a guest
// session throws its cookies/localStorage away on exit, which is how
// registrations used to end up with no reusable browser state.
func EnsureRegisterChrome(st *store.Store, log func(string)) bool {
	return ensureChromeOn(st, st.ProfileDir("reg-live"), RegisterURL(st), "", startPage, false, log)
}

// ManualURL is the dedicated debug endpoint for a manually opened account
// chrome (Accounts page) â€” kept OFF the workers' port range so a manual
// window never collides with worker 1.
func ManualURL(st *store.Store) string {
	return "http://127.0.0.1:9301"
}

// EnsureBotChrome guarantees a chrome with the account's profile is reachable
// on its dedicated manual-open port.
func EnsureBotChrome(st *store.Store, uid string, log func(string)) bool {
	return ensureChromeOn(st, st.ProfileDir(uid), ManualURL(st), "", startPage, false, log)
}

// EnsureBotChromeOn guarantees a chrome with the account's profile is
// reachable on the given attach URL â€” MULTI-BROWSER worker launch. When
// unique_ua_per_browser is enabled the account's UA is passed to chrome.
func EnsureBotChromeOn(st *store.Store, uid, attachURL string, log func(string)) bool {
	ua := ""
	if st.Bool("unique_ua_per_browser", false) {
		ua = st.UserAgentFor(uid)
	}
	return ensureChromeOn(st, st.ProfileDir(uid), attachURL, ua, startPage, false, log)
}

// ---- diagnostics -------------------------------------------------------------

// Status is the browser diagnostic block the dashboard renders: is chrome
// installed, which debug ports are live, and how many profiles exist.
func Status(st *store.Store) map[string]any {
	exe := chromeExe()
	_, exeErr := os.Stat(exe)
	chromeFound := exeErr == nil
	attach := st.Get("chrome_attach_url", "")
	if attach == "" {
		attach = "http://127.0.0.1:9222"
	}
	reg := RegisterURL(st)
	man := ManualURL(st)
	attachUp := DebugPortUpOn(attach)
	regUp := DebugPortUpOn(reg)
	manUp := DebugPortUpOn(man)

	root := rootOf(st)
	profiles := 0
	if entries, err := os.ReadDir(filepath.Join(root, "browser-profiles")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				profiles++
			}
		}
	}

	problem := ""
	switch {
	case !chromeFound:
		problem = "Chrome is not installed - run install.bat"
	case !attachUp && !regUp && !manUp:
		problem = "no browser window is open yet"
	}

	return map[string]any{
		"mode":         st.Get("bot_mode", "visible"),
		"chrome_exe":   exe,
		"chrome_found": chromeFound,
		"attach_url":   attach,
		"attach_up":    attachUp,
		"register_url": reg,
		"register_up":  regUp,
		"manual_url":   man,
		"manual_up":    manUp,
		"profiles":     profiles,
		"proxy_scheme": st.Get("proxy_scheme", st.Get("proxytype", "http")),
		"problem":      problem,
	}
}

// CloseAll shuts every chrome the dashboard owns (register, manual, and the
// worker base port). Workers on other ports are closed by StopBots.
func CloseAll(st *store.Store) {
	CloseChromeOn(RegisterURL(st))
	CloseChromeOn(ManualURL(st))
	if attach := st.Get("chrome_attach_url", ""); attach != "" {
		CloseChromeOn(attach)
	}
}
