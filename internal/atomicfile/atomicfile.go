// Package atomicfile replaces files so that readers, and the file after a
// crash, see either the old content or the new one, never a mix.
package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Write replaces path with data. A temporary file in the same directory is
// written and synced, then renamed over path, and the directory is synced so
// the rename survives a power loss.
func Write(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	// CreateTemp already makes the file 0600; not every filesystem supports
	// changing it, so a failure here is not fatal.
	_ = tmp.Chmod(perm)
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		d.Sync() // fails on some platforms (Windows); the rename is done anyway
		d.Close()
	}
	return nil
}
