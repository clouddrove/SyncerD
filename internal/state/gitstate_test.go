package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestPullRequestRecordRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "git-state.json")

	s := NewGit()
	s.MarkPR("gh-to-gl", "acme/widget", 7, PRRecord{
		DestNumber:    12,
		DestState:     "open",
		SourceUpdated: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
		CommentIDs:    map[string]string{"991": "551"},
	})
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadGit(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rec, ok := loaded.GetPR("gh-to-gl", "acme/widget", 7)
	if !ok {
		t.Fatal("record did not survive the round trip")
	}
	if rec.DestNumber != 12 || rec.DestState != "open" {
		t.Errorf("record = %+v", rec)
	}
	if rec.CommentIDs["991"] != "551" {
		t.Errorf("comment map lost: %+v", rec.CommentIDs)
	}
	if !rec.SourceUpdated.Equal(time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("SourceUpdated = %v", rec.SourceUpdated)
	}
}

func TestStateFileWithoutPullRequestsLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old-state.json")
	// A file written before pull request mirroring existed.
	body := `{"version":1,"mirrored":{"m":{"acme/widget":{"fingerprint":"abc","dest_path":"widget"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := LoadGit(path)
	if err != nil {
		t.Fatalf("an older state file must still load: %v", err)
	}
	if _, ok := loaded.GetPR("m", "acme/widget", 1); ok {
		t.Error("there should be no pull request records")
	}
	if _, ok := loaded.Get("m", "acme/widget"); !ok {
		t.Error("the existing repository state must survive")
	}
}

func TestForgetPRCausesReinspection(t *testing.T) {
	s := NewGit()
	s.MarkPR("m", "acme/widget", 7, PRRecord{DestNumber: 3})
	s.ForgetPR("m", "acme/widget", 7)
	if _, ok := s.GetPR("m", "acme/widget", 7); ok {
		t.Error("the record should be gone")
	}
}
