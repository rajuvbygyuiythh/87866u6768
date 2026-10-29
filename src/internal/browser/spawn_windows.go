//go:build windows

package browser

import (
	"os/exec"
)

// spawnDetached starts chrome so it outlives the dashboard (the bot process
// can exit without killing the browser). Chrome is a GUI app: it must NOT be
// started with HideWindow/DETACHED_PROCESS — those console-oriented flags made
// the spawned chrome die instantly (its window was suppressed and the browser
// process exited before the debug port ever came up, which is exactly how
// "Run Browser" used to open nothing). chromedp's own exec path starts chrome
// with no creation flags either.
// env is the clean child environment from childEnv(); dir must exist.
func spawnDetached(path string, args []string, dir string, env []string) error {
	cmd := exec.Command(path, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	return cmd.Start()
}
