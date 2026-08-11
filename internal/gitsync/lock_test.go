package gitsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireLockCreatesAndReleases(t *testing.T) {
	dir := t.TempDir()

	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Releasing twice must not error, so deferred cleanup is safe.
	if err := l.Release(); err != nil {
		t.Errorf("second release: %v", err)
	}
}

func TestAcquireLockRejectsSecondHolder(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = AcquireLock(dir)
	if err == nil {
		t.Fatal("expected the second acquire to fail")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error should explain the conflict, got %v", err)
	}
}

func TestAcquireLockReclaimsAfterRelease(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	_ = second.Release()
}

func TestAcquireLockCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")

	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_ = l.Release()
}

func TestReleaseLeavesLockFileInPlace(t *testing.T) {
	dir := t.TempDir()

	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Unlinking would let a later process lock a different inode at the
	// same path while an existing holder still has the old one.
	if _, err := os.Stat(filepath.Join(dir, ".syncerd-git.lock")); err != nil {
		t.Errorf("the lock file must remain on disk after release: %v", err)
	}
}
