//go:build !windows

package browser

import "os/exec"

// spawnDetached starts a process without waiting for it (non-Windows builds are
// not shipped; this keeps the package compiling for tooling).
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
