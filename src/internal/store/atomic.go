package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic replaces path with data without exposing a partially written
// file. The temporary file lives in the destination directory so the final
// rename stays on the same filesystem. Existing permission bits are preserved;
// perm is used only when the destination does not exist yet.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmp := f.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp for %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync temp for %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	renamed = true
	return nil
}
