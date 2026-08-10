package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// git runs a git command in dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newSourceRepo builds a bare repo with one commit on main and tag v1.0.0.
func newSourceRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "source.git")

	git(t, root, "init", "--bare", "--initial-branch=main", bare)
	git(t, root, "init", "--initial-branch=main", work)
	if err := writeFile(filepath.Join(work, "README.md"), "hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "initial")
	git(t, work, "tag", "v1.0.0")
	git(t, work, "remote", "add", "origin", bare)
	git(t, work, "push", "origin", "main", "--tags")

	return bare
}

func newEmptyBare(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "dest.git")
	git(t, root, "init", "--bare", "--initial-branch=main", bare)
	return bare
}

var noCred = vcs.GitCredential{}

func TestPreflight(t *testing.T) {
	r := NewRunner(nil)
	if err := r.Preflight(context.Background()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}

func TestLsRemoteReadsRefs(t *testing.T) {
	src := newSourceRepo(t)
	r := NewRunner(nil)

	refs, err := r.LsRemote(context.Background(), src, noCred)
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}

	var names []string
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "refs/heads/main") {
		t.Errorf("missing main in %q", joined)
	}
	if !strings.Contains(joined, "refs/tags/v1.0.0") {
		t.Errorf("missing tag in %q", joined)
	}
}

func TestLsRemoteEmptyRepo(t *testing.T) {
	r := NewRunner(nil)
	refs, err := r.LsRemote(context.Background(), newEmptyBare(t), noCred)
	if err != nil {
		t.Fatalf("ls-remote on empty repo must not error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("got %d refs, want 0", len(refs))
	}
}

func TestSyncCacheClonesThenUpdates(t *testing.T) {
	src := newSourceRepo(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("first SyncCache: %v", err)
	}
	// Second call takes the update path rather than cloning again.
	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("second SyncCache: %v", err)
	}
}

func TestPushMirrorCopiesBranchesAndTags(t *testing.T) {
	src := newSourceRepo(t)
	dest := newEmptyBare(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("cache: %v", err)
	}
	res, err := r.Push(ctx, cache, dest, noCred, PushMirror, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Pushed == 0 {
		t.Errorf("expected pushed refs, got %+v", res)
	}

	refs, err := r.LsRemote(ctx, dest, noCred)
	if err != nil {
		t.Fatalf("verify ls-remote: %v", err)
	}
	var joined string
	for _, ref := range refs {
		joined += ref.Name + ","
	}
	if !strings.Contains(joined, "refs/heads/main") {
		t.Errorf("destination missing main: %q", joined)
	}
	if !strings.Contains(joined, "refs/tags/v1.0.0") {
		t.Errorf("destination missing tag: %q", joined)
	}
}

func TestPushMirrorPrunesDeletedBranch(t *testing.T) {
	src := newSourceRepo(t)
	dest := newEmptyBare(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	// Add a second branch, mirror it, then delete it at source.
	srcWork := filepath.Join(t.TempDir(), "w")
	git(t, filepath.Dir(srcWork), "clone", src, srcWork)
	git(t, srcWork, "checkout", "-b", "feature")
	git(t, srcWork, "commit", "--allow-empty", "-m", "feature work")
	git(t, srcWork, "push", "origin", "feature")

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("cache: %v", err)
	}
	if _, err := r.Push(ctx, cache, dest, noCred, PushMirror, false); err != nil {
		t.Fatalf("first push: %v", err)
	}

	git(t, srcWork, "push", "origin", "--delete", "feature")

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("recache: %v", err)
	}
	res, err := r.Push(ctx, cache, dest, noCred, PushMirror, false)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if res.Deleted == 0 {
		t.Errorf("expected a pruned ref, got %+v", res)
	}

	refs, _ := r.LsRemote(ctx, dest, noCred)
	for _, ref := range refs {
		if ref.Name == "refs/heads/feature" {
			t.Error("feature branch should have been pruned at destination")
		}
	}
}

func TestPushAdditiveDoesNotPrune(t *testing.T) {
	src := newSourceRepo(t)
	dest := newEmptyBare(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	srcWork := filepath.Join(t.TempDir(), "w")
	git(t, filepath.Dir(srcWork), "clone", src, srcWork)
	git(t, srcWork, "checkout", "-b", "feature")
	git(t, srcWork, "commit", "--allow-empty", "-m", "feature work")
	git(t, srcWork, "push", "origin", "feature")

	_ = r.SyncCache(ctx, src, cache, noCred)
	_, _ = r.Push(ctx, cache, dest, noCred, PushAdditive, false)

	git(t, srcWork, "push", "origin", "--delete", "feature")
	_ = r.SyncCache(ctx, src, cache, noCred)
	res, err := r.Push(ctx, cache, dest, noCred, PushAdditive, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Deleted != 0 {
		t.Errorf("additive mode must never delete, got %+v", res)
	}

	refs, _ := r.LsRemote(ctx, dest, noCred)
	found := false
	for _, ref := range refs {
		if ref.Name == "refs/heads/feature" {
			found = true
		}
	}
	if !found {
		t.Error("additive mode must leave the destination branch in place")
	}
}

func TestPushDryRunWritesNothing(t *testing.T) {
	src := newSourceRepo(t)
	dest := newEmptyBare(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("cache: %v", err)
	}
	res, err := r.Push(ctx, cache, dest, noCred, PushMirror, true)
	if err != nil {
		t.Fatalf("dry run push: %v", err)
	}
	if res.Pushed == 0 {
		t.Error("dry run should still report what it would push")
	}

	refs, _ := r.LsRemote(ctx, dest, noCred)
	if len(refs) != 0 {
		t.Errorf("dry run must not write to the destination, found %d refs", len(refs))
	}
}

func TestParsePushMode(t *testing.T) {
	for _, s := range []string{"mirror", "additive", "fast-forward"} {
		if _, err := ParsePushMode(s); err != nil {
			t.Errorf("ParsePushMode(%q): %v", s, err)
		}
	}
	if m, err := ParsePushMode(""); err != nil || m != PushMirror {
		t.Errorf("empty push_mode must default to mirror, got %q %v", m, err)
	}
	if _, err := ParsePushMode("nonsense"); err == nil {
		t.Error("expected error for an unknown push mode")
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
