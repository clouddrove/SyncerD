//go:build !windows

package gitsync

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUnlockFileActuallyReleases proves unlockFile does the work, rather
// than relying on the close that Release performs immediately afterwards.
func TestUnlockFileActuallyReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.lock")

	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer func() { _ = first.Close() }()

	second, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()

	if err := lockFile(first); err != nil {
		t.Fatalf("lock first: %v", err)
	}

	// While the first holds it, the second must be refused.
	if err := lockFile(second); err == nil {
		t.Fatal("a second exclusive lock on the same file must be refused")
	}

	// After unlocking, and with the first handle still OPEN, the second
	// must succeed. If unlockFile were a no op this would still fail.
	if err := unlockFile(first); err != nil {
		t.Fatalf("unlock first: %v", err)
	}
	if err := lockFile(second); err != nil {
		t.Errorf("after unlock the second lock must succeed, got %v", err)
	}
	if err := unlockFile(second); err != nil {
		t.Errorf("unlock second: %v", err)
	}
}
