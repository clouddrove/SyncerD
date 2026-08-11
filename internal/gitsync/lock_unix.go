//go:build !windows

package gitsync

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive, non blocking advisory lock on f using flock.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the flock held by lockFile.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
