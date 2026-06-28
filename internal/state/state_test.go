package state

import (
	"path/filepath"
	"testing"
)

func TestStateMarkAndIsSynced(t *testing.T) {
	s := New()
	if s.IsSynced("ecr", "library/nginx", "1.25") {
		t.Fatal("fresh state should not report synced")
	}
	s.MarkSynced("ecr", "library/nginx", "1.25")
	if !s.IsSynced("ecr", "library/nginx", "1.25") {
		t.Fatal("expected marked tag to be synced")
	}
	// Unrelated dest/image/tag must stay unsynced.
	if s.IsSynced("acr", "library/nginx", "1.25") {
		t.Fatal("other destination must not be synced")
	}
	if s.IsSynced("ecr", "library/redis", "1.25") {
		t.Fatal("other image must not be synced")
	}
	if s.IsSynced("ecr", "library/nginx", "1.26") {
		t.Fatal("other tag must not be synced")
	}
}

func TestStateNilSafe(t *testing.T) {
	var s *State
	if s.IsSynced("ecr", "library/nginx", "1.25") {
		t.Fatal("nil state IsSynced must return false")
	}
}

func TestStateSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")

	s := New()
	s.MarkSynced("ecr", "library/nginx", "1.25")
	s.MarkSynced("acr", "clouddrove/syncerd", "latest")
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.IsSynced("ecr", "library/nginx", "1.25") {
		t.Error("round-trip lost ecr/library/nginx:1.25")
	}
	if !loaded.IsSynced("acr", "clouddrove/syncerd", "latest") {
		t.Error("round-trip lost acr/clouddrove/syncerd:latest")
	}
}

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if s == nil || s.Synced == nil {
		t.Fatal("expected initialized empty state")
	}
}
