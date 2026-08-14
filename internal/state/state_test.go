package state

import (
	"os"
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

// TestSaveRenameFailureRemovesTmpFile checks that a rename failure, such as
// the destination path already existing as a directory, does not leave a
// stray .tmp file behind. Left alone, every subsequent save pointed at the
// same misconfigured path would add another one. This mirrors the same fix
// already applied to internal/runreport.Write.
func TestSaveRenameFailureRemovesTmpFile(t *testing.T) {
	dir := t.TempDir()
	// path is a directory, so os.WriteFile(tmp, ...) succeeds but
	// os.Rename(tmp, path) fails because you cannot rename a file onto an
	// existing directory.
	path := filepath.Join(dir, "state.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s := New()
	s.MarkSynced("ecr", "library/nginx", "1.25")
	if err := s.Save(path); err == nil {
		t.Fatal("expected an error saving to a path that is a directory")
	}

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no .tmp file left behind after a failed rename, stat err = %v", err)
	}
}
