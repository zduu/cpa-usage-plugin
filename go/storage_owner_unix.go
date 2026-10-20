//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockStorageFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
