package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Keep the file in place: unlinking a locked file would let another instance
// lock a different inode. Closing the handle releases ownership after a crash
// or a completed quiesce without leaving a stale logical lock behind.
func acquireStorageDirectory(dir string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(dir, ".writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage writer lock: %w", err)
	}
	if err := lockStorageFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire storage writer lock for %s (another instance may be writing): %w", dir, err)
	}
	return file, nil
}
