package gitsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Lock is an advisory lock over the clone cache directory. It prevents a
// second SyncerD process from corrupting the cache, which happens when a
// scheduled run overruns and the next one starts.
type Lock struct {
	path     string
	file     *os.File
	released bool
}

// AcquireLock takes an exclusive, non blocking lock on dir. The directory
// is created if missing. The underlying platform lock is implemented in
// lock_unix.go and lock_windows.go so the exported API is identical on
// every platform.
func AcquireLock(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}

	path := filepath.Join(dir, ".syncerd-git.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another git-sync is already running against work_dir %q (lock: %s)", dir, path)
	}

	// Record the pid for operators debugging a stuck run.
	_ = f.Truncate(0)
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		// Not fatal: the lock is held by the operating system, not by the
		// file contents.
		_ = err
	}

	return &Lock{path: path, file: f}, nil
}

// Release drops the lock. Calling it more than once is safe.
//
// The lock file is deliberately left on disk. Unlinking it would let a
// later process create a fresh inode at the same path and lock that
// instead, so two processes could hold what each believes is an exclusive
// lock. An empty lock file in the cache directory costs nothing.
func (l *Lock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true

	if err := unlockFile(l.file); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("unlock: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close lock file: %w", err)
	}
	return nil
}
