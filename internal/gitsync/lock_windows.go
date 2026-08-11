//go:build windows

package gitsync

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive, non blocking advisory lock on f using
// LockFileEx. The locked range covers the whole file regardless of its
// actual size, matching the flock semantics used on unix.
func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0),
		^uint32(0),
		&overlapped,
	)
}

// unlockFile releases the lock held by lockFile. Windows also releases a
// file lock automatically when the handle closes, so an already released
// lock is tolerated rather than treated as an error.
func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		^uint32(0),
		^uint32(0),
		&overlapped,
	)
	if err == windows.ERROR_NOT_LOCKED {
		return nil
	}
	return err
}
