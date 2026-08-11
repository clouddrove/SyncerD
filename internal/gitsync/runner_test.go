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

// git runs a git command in dir and fails the test on error. The extra
// GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM entries make the fixture
// hermetic: a developer's global or system git config cannot change what
// these tests observe.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
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
	if err := testWriteFile(filepath.Join(work, "README.md"), "hello\n"); err != nil {
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

func TestSyncCacheFetchesOnlyBranchesAndTags(t *testing.T) {
	src := newSourceRepo(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	// Simulate a host internal namespace such as refs/pull or
	// refs/merge_requests, which must never reach the cache.
	head := strings.TrimSpace(git(t, src, "rev-parse", "refs/heads/main"))
	git(t, src, "update-ref", "refs/pull/1/head", head)

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("cache: %v", err)
	}

	out := git(t, cache, "for-each-ref", "--format=%(refname)")
	if strings.Contains(out, "refs/pull/") {
		t.Errorf("cache fetched a host internal namespace:\n%s", out)
	}
	if !strings.Contains(out, "refs/heads/main") {
		t.Errorf("cache is missing the branch it should have fetched:\n%s", out)
	}
	if !strings.Contains(out, "refs/tags/v1.0.0") {
		t.Errorf("cache is missing the tag it should have fetched:\n%s", out)
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

func TestPushMirrorRefusesEmptyCache(t *testing.T) {
	dest := newSourceRepo(t) // destination deliberately has content
	cache := filepath.Join(t.TempDir(), "empty.git")
	r := NewRunner(nil)
	ctx := context.Background()

	git(t, filepath.Dir(cache), "init", "--bare", "--initial-branch=main", cache)

	if _, err := r.Push(ctx, cache, dest, noCred, PushMirror, false); err == nil {
		t.Fatal("mirror push from an empty cache must be refused")
	}

	refs, err := r.LsRemote(ctx, dest, noCred)
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("destination refs were destroyed by a push that should have been refused")
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

func TestPushFastForwardRefusesRewrite(t *testing.T) {
	src := newSourceRepo(t)
	dest := newEmptyBare(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("cache: %v", err)
	}
	if _, err := r.Push(ctx, cache, dest, noCred, PushFastForward, false); err != nil {
		t.Fatalf("initial fast-forward push: %v", err)
	}

	before, err := r.LsRemote(ctx, dest, noCred)
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}

	// Rewrite history at the source so the next push is not a fast
	// forward. Amending the existing commit produces a new commit with
	// the same parent (none) as the original, so it is not a descendant
	// of what is already at the destination: adding a new commit on top
	// with --allow-empty would still be a fast forward and would not
	// exercise this path.
	work := filepath.Join(t.TempDir(), "rewrite")
	git(t, filepath.Dir(work), "clone", src, work)
	git(t, work, "commit", "--amend", "-m", "rewritten")
	git(t, work, "push", "--force", "origin", "HEAD:main")

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("recache: %v", err)
	}
	if _, err := r.Push(ctx, cache, dest, noCred, PushFastForward, false); err == nil {
		t.Fatal("fast-forward mode must refuse a non fast forward update")
	}

	after, err := r.LsRemote(ctx, dest, noCred)
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("ref count changed: before %d, after %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("destination ref changed despite a refused push: %+v became %+v", before[i], after[i])
		}
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

func TestPushFailureReturnsError(t *testing.T) {
	src := newSourceRepo(t)
	cache := filepath.Join(t.TempDir(), "cache.git")
	r := NewRunner(nil)
	ctx := context.Background()

	if err := r.SyncCache(ctx, src, cache, noCred); err != nil {
		t.Fatalf("cache: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "no-such-repo.git")
	if _, err := r.Push(ctx, cache, missing, noCred, PushAdditive, false); err == nil {
		t.Fatal("pushing to a nonexistent destination must return an error")
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

func TestRejectsURLWithEmbeddedCredentials(t *testing.T) {
	r := NewRunner(nil)
	ctx := context.Background()
	bad := "https://oauth2:sometoken@example.com/a/b.git"

	if _, err := r.LsRemote(ctx, bad, noCred); err == nil {
		t.Error("LsRemote must reject a URL with embedded credentials")
	}
	if err := r.SyncCache(ctx, bad, filepath.Join(t.TempDir(), "c.git"), noCred); err == nil {
		t.Error("SyncCache must reject a URL with embedded credentials")
	}
	if _, err := r.Push(ctx, t.TempDir(), bad, noCred, PushAdditive, true); err == nil {
		t.Error("Push must reject a URL with embedded credentials")
	}
}

func TestParseGitVersion(t *testing.T) {
	cases := map[string][2]int{
		"git version 2.30.0\n":               {2, 30},
		"git version 2.55.0":                 {2, 55},
		"git version 2.39.3 (Apple Git-146)": {2, 39},
	}
	for out, want := range cases {
		major, minor, err := parseGitVersion(out)
		if err != nil {
			t.Errorf("parseGitVersion(%q): %v", out, err)
			continue
		}
		if major != want[0] || minor != want[1] {
			t.Errorf("parseGitVersion(%q) = %d.%d, want %d.%d", out, major, minor, want[0], want[1])
		}
	}

	for _, bad := range []string{"", "git", "git version", "git version x.y"} {
		if _, _, err := parseGitVersion(bad); err == nil {
			t.Errorf("parseGitVersion(%q) must fail", bad)
		}
	}
}

func TestParsePorcelain(t *testing.T) {
	out := "To https://example.com/a/b.git\n" +
		"*\trefs/heads/main:refs/heads/main\t[new branch]\n" +
		" \trefs/heads/next:refs/heads/next\t1f7b4c3..a88df43\n" +
		"+\trefs/heads/force:refs/heads/force\t29d1313...97548bf (forced update)\n" +
		"=\trefs/tags/v1:refs/tags/v1\t[up to date]\n" +
		"-\t:refs/heads/gone\t[deleted]\n" +
		"Done\n"

	res := parsePorcelain(out)
	if res.Pushed != 3 {
		t.Errorf("Pushed = %d, want 3", res.Pushed)
	}
	if res.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", res.Deleted)
	}
}

func TestParsePorcelainDoesNotCountRejected(t *testing.T) {
	out := "To https://example.com/a/b.git\n" +
		"!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)\n" +
		"Done\n"

	res := parsePorcelain(out)
	if res.Pushed != 0 || res.Deleted != 0 {
		t.Errorf("a rejected ref must not count as pushed or deleted, got %+v", res)
	}
}

func TestCredEnvKeepsSecretsOutOfConfigCount(t *testing.T) {
	basic := credEnv(vcs.GitCredential{Kind: vcs.CredBasic, User: "x-access-token", Secret: "ghp_supersecret"}, "https://github.com/a/b.git")

	var count string
	keys := 0
	for _, kv := range basic {
		switch {
		case strings.HasPrefix(kv, "GIT_CONFIG_COUNT="):
			count = strings.TrimPrefix(kv, "GIT_CONFIG_COUNT=")
		case strings.HasPrefix(kv, "GIT_CONFIG_KEY_"):
			keys++
		}
	}
	if count != "3" || keys != 3 {
		t.Errorf("GIT_CONFIG_COUNT = %q with %d keys; count must match the number of keys", count, keys)
	}

	// The secret may appear only as an env value git reads by reference.
	joined := strings.Join(basic, "\n")
	if !strings.Contains(joined, "SYNCERD_GIT_SECRET=ghp_supersecret") {
		t.Error("secret must be passed through the environment")
	}
	if strings.Contains(joined, "GIT_CONFIG_VALUE_1=ghp_supersecret") {
		t.Error("secret must not be inlined into a config value")
	}
	if !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("basic auth must clear inherited global git config, got:\n%s", joined)
	}
}

func TestCredEnvScopesBearerHeaderToHost(t *testing.T) {
	env := credEnv(vcs.GitCredential{Kind: vcs.CredBearer, Secret: "entra-token-value"}, "https://dev.azure.com/org/proj/_git/repo")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.https://dev.azure.com/.extraHeader") {
		t.Errorf("bearer header must be scoped to the host, got:\n%s", joined)
	}
	if !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("bearer auth must clear inherited global git config, so a stray http.extraHeader cannot ride along, got:\n%s", joined)
	}
}

func TestCredEnvWithoutCredentialClearsInheritedConfig(t *testing.T) {
	env := credEnv(vcs.GitCredential{}, "https://example.com/a/b.git")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=0") {
		t.Errorf("an empty credential must clear inherited GIT_CONFIG, got:\n%s", joined)
	}
	if !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("the no credential path must also clear inherited global git config, got:\n%s", joined)
	}
}

func TestValidateCredentialRejectsNewline(t *testing.T) {
	if err := validateCredential(vcs.GitCredential{Secret: "good\nquit=1"}); err == nil {
		t.Error("a secret containing a newline must be rejected")
	}
	if err := validateCredential(vcs.GitCredential{User: "bad\nuser", Secret: "fine"}); err == nil {
		t.Error("a username containing a newline must be rejected")
	}
	if err := validateCredential(vcs.GitCredential{User: "x-access-token", Secret: "ghp_fine"}); err != nil {
		t.Errorf("a clean credential must be accepted, got %v", err)
	}
}

func testWriteFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
