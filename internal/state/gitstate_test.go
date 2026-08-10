package state

import (
	"path/filepath"
	"testing"
)

func TestGitStateMarkAndGet(t *testing.T) {
	s := NewGit()
	if _, ok := s.Get("gh-to-gl", "clouddrove/vpc"); ok {
		t.Fatal("fresh state must not report a mirrored repo")
	}

	s.Mark("gh-to-gl", "clouddrove/vpc", "mirrors/vpc", "abc123")

	got, ok := s.Get("gh-to-gl", "clouddrove/vpc")
	if !ok {
		t.Fatal("expected marked repo to be present")
	}
	if got.Fingerprint != "abc123" {
		t.Errorf("Fingerprint = %q, want abc123", got.Fingerprint)
	}
	if got.DestPath != "mirrors/vpc" {
		t.Errorf("DestPath = %q, want mirrors/vpc", got.DestPath)
	}
	if got.SyncedAt.IsZero() {
		t.Error("SyncedAt must be stamped")
	}

	if _, ok := s.Get("other-mirror", "clouddrove/vpc"); ok {
		t.Error("a different mirror must not share state")
	}
	if _, ok := s.Get("gh-to-gl", "clouddrove/other"); ok {
		t.Error("a different repo must not share state")
	}
}

func TestGitStateNilSafe(t *testing.T) {
	var s *GitState
	if _, ok := s.Get("m", "r"); ok {
		t.Fatal("nil GitState Get must return false")
	}
}

func TestGitStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "git-state.json")

	s := NewGit()
	s.Mark("gh-to-gl", "clouddrove/vpc", "mirrors/vpc", "abc123")
	s.Mark("gl-to-gh", "mirrors/eks", "clouddrove/eks", "def456")
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadGit(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := loaded.Get("gh-to-gl", "clouddrove/vpc")
	if !ok || got.Fingerprint != "abc123" {
		t.Errorf("round trip lost gh-to-gl entry: %+v ok=%v", got, ok)
	}
	if _, ok := loaded.Get("gl-to-gh", "mirrors/eks"); !ok {
		t.Error("round trip lost gl-to-gh entry")
	}
}

func TestLoadGitMissingFile(t *testing.T) {
	s, err := LoadGit(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if s == nil || s.Mirrored == nil {
		t.Fatal("expected initialized empty git state")
	}
}

func TestGitStateSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "git-state.json")

	s := NewGit()
	s.Mark("m", "r", "d", "fp")
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temp file left behind: %v", entries)
	}
}
