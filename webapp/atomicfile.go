package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path via a temporary file in the same
// directory followed by a rename. Keeping the temp file alongside the target
// guarantees both live on the same filesystem, so the rename is atomic even
// when path sits on a Docker bind mount.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	cleanup := func(cause error) error {
		tmp.Close()
		os.Remove(tmpName)
		return cause
	}

	// Chmod explicitly: CreateTemp always uses 0600, which is right for the
	// secret-bearing files but wrong for the ones meant to be world-readable.
	if err := tmp.Chmod(perm); err != nil {
		return cleanup(fmt.Errorf("chmod %s: %w", tmpName, err))
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("write %s: %w", tmpName, err))
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("sync %s: %w", tmpName, err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}

	// Fsync the directory so the rename itself is durable, not just the data.
	// A failure here means the content is written but the directory entry may
	// not survive a crash; that is not worth failing the whole write over.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
