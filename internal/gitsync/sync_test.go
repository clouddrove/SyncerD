package gitsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
)

// fakeRemote serves local bare repositories as if they were a provider.
type fakeRemote struct {
	base    string // directory holding <name>.git
	nesting bool
}

func (f *fakeRemote) CloneURL(path string) string { return filepath.Join(f.base, path+".git") }
func (f *fakeRemote) GitCredential(context.Context) (vcs.GitCredential, error) {
	return vcs.GitCredential{}, nil
}
func (f *fakeRemote) SupportsNesting() bool { return f.nesting }

// fakeLister returns a fixed repo list.
type fakeLister struct{ repos []vcs.Repo }

func (f *fakeLister) ListRepos(context.Context) ([]vcs.Repo, error) { return f.repos, nil }

// panicRemote panics when the engine asks for a git credential, standing in
// for any provider bug that would take down a worker. GitCredential is used
// rather than CloneURL because the engine now sources a source repository's
// clone URL from vcs.Repo rather than calling SourceRemote.CloneURL.
type panicRemote struct{ fakeRemote }

func (p *panicRemote) GitCredential(context.Context) (vcs.GitCredential, error) {
	panic("provider exploded")
}

// fakeEnsurer records creations and initialises a bare repo on disk. The
// engine's worker pool can call EnsureRepo from several goroutines at
// once, so created is guarded by a mutex like a real provider client
// would need to be.
type fakeEnsurer struct {
	base string
	t    *testing.T

	mu      sync.Mutex
	created []string
}

func (f *fakeEnsurer) EnsureRepo(_ context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	dir := filepath.Join(f.base, spec.Path+".git")
	git(f.t, f.base, "init", "--bare", "--initial-branch=main", dir)
	f.mu.Lock()
	f.created = append(f.created, spec.Path)
	f.mu.Unlock()
	return vcs.Repo{Name: spec.Path, Path: spec.Path}, nil
}

func newEngineFixture(t *testing.T) (*Engine, Mirror, *fakeEnsurer) {
	t.Helper()

	srcBase := t.TempDir()
	dstBase := t.TempDir()

	// A source repo named "app" under owner "acme", with one commit and a
	// tag. The bare repo lives at the nested path matching Repo.Path
	// ("acme/app"); its CloneURL is set on the vcs.Repo below the same way
	// a real provider's ListRepos would report it, since the engine now
	// uses repo.CloneURL for the source rather than recomputing it.
	work := filepath.Join(t.TempDir(), "work")
	bare := filepath.Join(srcBase, "acme", "app.git")
	git(t, srcBase, "init", "--bare", "--initial-branch=main", bare)
	git(t, filepath.Dir(work), "init", "--initial-branch=main", work)
	if err := testWriteFile(filepath.Join(work, "README.md"), "hi\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "initial")
	git(t, work, "tag", "v1.0.0")
	git(t, work, "remote", "add", "origin", bare)
	git(t, work, "push", "origin", "main", "--tags")

	names, err := vcs.ParseNameTemplate("")
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	ensurer := &fakeEnsurer{base: dstBase, t: t}
	srcRemote := &fakeRemote{base: srcBase, nesting: true}

	m := Mirror{
		Name: "fake",
		Source: &fakeLister{repos: []vcs.Repo{
			{Name: "app", Owner: "acme", Path: "acme/app", DefaultBranch: "main", CloneURL: srcRemote.CloneURL("acme/app")},
		}},
		SourceRemote:  srcRemote,
		Dest:          &fakeRemote{base: dstBase, nesting: true},
		DestEnsurer:   ensurer,
		Names:         names,
		PushMode:      PushMirror,
		CreateMissing: true,
		Visibility:    "private",
	}

	eng := NewEngine(EngineOptions{
		WorkDir:     t.TempDir(),
		Concurrency: 2,
		State:       state.NewGit(),
		StatePath:   filepath.Join(t.TempDir(), "git-state.json"),
		Runner:      NewRunner(nil),
	})
	return eng, m, ensurer
}

func TestEngineMirrorsRepo(t *testing.T) {
	eng, m, ensurer := newEngineFixture(t)

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if len(rep.Mirrored) != 1 {
		t.Fatalf("got %d mirrored, want 1: %+v", len(rep.Mirrored), rep.Mirrored)
	}
	if !rep.Mirrored[0].Created {
		t.Error("expected the destination to be created")
	}
	if len(ensurer.created) != 1 || ensurer.created[0] != "app" {
		t.Errorf("ensurer created %v, want [app]", ensurer.created)
	}
}

func TestEngineSkipsUnchangedRepoOnSecondRun(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	ctx := context.Background()

	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	rep, err := eng.Run(ctx, []Mirror{m})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if rep.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", rep.Skipped)
	}
	if len(rep.Mirrored) != 0 {
		t.Errorf("unchanged repo must not be re-mirrored: %+v", rep.Mirrored)
	}
}

func TestEngineDryRunWritesNothing(t *testing.T) {
	eng, m, ensurer := newEngineFixture(t)
	eng.opts.DryRun = true

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}

	if len(ensurer.created) != 0 {
		t.Errorf("dry run created destination repositories: %v", ensurer.created)
	}
	if _, ok := eng.opts.State.Get("fake", "acme/app"); ok {
		t.Error("dry run must not record state")
	}
	if _, err := os.Stat(eng.opts.StatePath); err == nil {
		t.Error("dry run must not write the state file")
	}
	if len(rep.Mirrored) != 1 {
		t.Errorf("a dry run should still report what it would do, got %+v", rep.Mirrored)
	}
	if !rep.Mirrored[0].Created {
		t.Error("a dry run against a missing destination should report it would be created")
	}
}

func TestEngineFiltersRepos(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	m.Filter = vcs.Filter{Include: []string{"nomatch-*"}}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Mirrored) != 0 || rep.Skipped != 0 {
		t.Errorf("filtered repo should not be processed: %+v", rep)
	}
}

func TestEngineAdoptGuardRefusesNonEmptyDestination(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	ctx := context.Background()

	// Mirror once so the destination has content, then wipe state to
	// simulate a first run against a populated destination.
	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	eng.opts.State = state.NewGit()

	rep, err := eng.Run(ctx, []Mirror{m})
	if err == nil {
		t.Fatal("expected the adopt guard to fail the run")
	}
	if len(rep.Failures) != 1 {
		t.Fatalf("got %d failures, want 1: %+v", len(rep.Failures), rep.Failures)
	}
	if rep.Failures[0].Stage != "adopt" {
		t.Errorf("Stage = %q, want adopt", rep.Failures[0].Stage)
	}
}

func TestEngineAdoptOverwritesWhenOptedIn(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	ctx := context.Background()

	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	eng.opts.State = state.NewGit()
	m.Adopt = true

	rep, err := eng.Run(ctx, []Mirror{m})
	if err != nil {
		t.Fatalf("adopt run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("adopt true must bypass the guard: %+v", rep.Failures)
	}
	// Prove the guard was the thing bypassed, not that nothing happened.
	if len(rep.Mirrored) != 1 {
		t.Fatalf("adopt run should have mirrored the repository, got %+v", rep.Mirrored)
	}
	if _, ok := eng.opts.State.Get(m.Name, "acme/app"); !ok {
		t.Error("adopt run should have recorded state")
	}
}

func TestEngineUsesRepoCloneURLForSource(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	// A source whose reported clone URL is wrong must fail at fetch rather
	// than silently succeeding through a recomputed path.
	m.Source = &fakeLister{repos: []vcs.Repo{
		{Name: "app", Owner: "acme", Path: "acme/app", DefaultBranch: "main",
			CloneURL: filepath.Join(t.TempDir(), "nowhere.git")},
	}}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err == nil {
		t.Fatal("expected the run to fail when the source clone URL does not resolve")
	}
	if len(rep.Failures) != 1 || rep.Failures[0].Stage != "fetch" {
		t.Fatalf("expected one fetch failure, got %+v", rep.Failures)
	}
}

func TestEngineFailsWhenSourceReportsNoCloneURL(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	m.Source = &fakeLister{repos: []vcs.Repo{
		{Name: "app", Owner: "acme", Path: "acme/app", DefaultBranch: "main"},
	}}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err == nil {
		t.Fatal("expected the run to fail when the source reports no clone URL")
	}
	if len(rep.Failures) != 1 || rep.Failures[0].Stage != "fetch" {
		t.Fatalf("expected one fetch failure, got %+v", rep.Failures)
	}
}

func TestEngineFailFastStopsOnFirstFailure(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	eng.opts.FailFast = true

	// A source repo whose reported clone URL does not exist on disk fails
	// at the fetch stage.
	m.Source = &fakeLister{repos: []vcs.Repo{
		{Name: "missing", Owner: "acme", Path: "acme/missing", DefaultBranch: "main",
			CloneURL: m.SourceRemote.CloneURL("acme/missing")},
	}}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err == nil {
		t.Fatal("FailFast must return an error when a repository fails")
	}
	if len(rep.Failures) == 0 {
		t.Fatal("expected a recorded failure")
	}
}

func TestEngineFailFastAbortsOnWorkerPanic(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	eng.opts.FailFast = true
	m.SourceRemote = &panicRemote{}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err == nil {
		t.Fatal("a panicking worker must fail the run under FailFast")
	}
	if len(rep.Failures) != 1 {
		t.Fatalf("got %d failures, want 1: %+v", len(rep.Failures), rep.Failures)
	}
	if rep.Failures[0].Stage != "worker" {
		t.Errorf("Stage = %q, want worker", rep.Failures[0].Stage)
	}
}

func TestEngineRefusesCollidingDestinationNames(t *testing.T) {
	eng, m, ensurer := newEngineFixture(t)

	// Two distinct source repositories whose default template renders the
	// same destination name.
	m.Source = &fakeLister{repos: []vcs.Repo{
		{Name: "docs", Owner: "acme", Path: "acme/team-a/docs", DefaultBranch: "main", CloneURL: "unused"},
		{Name: "docs", Owner: "acme", Path: "acme/team-b/docs", DefaultBranch: "main", CloneURL: "unused"},
	}}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err == nil {
		t.Fatal("colliding destination names must fail the mirror")
	}
	if len(rep.Failures) != 1 || rep.Failures[0].Stage != "discover" {
		t.Fatalf("expected one discover failure, got %+v", rep.Failures)
	}
	if len(ensurer.created) != 0 {
		t.Errorf("nothing should have been created: %v", ensurer.created)
	}
	if len(rep.Mirrored) != 0 {
		t.Errorf("nothing should have been mirrored: %+v", rep.Mirrored)
	}
}

func TestCacheKeyIsCollisionFree(t *testing.T) {
	a := cacheKey(vcs.Repo{Path: "group/sub/repo"})
	b := cacheKey(vcs.Repo{Path: "group/sub__repo"})
	if a == b {
		t.Fatalf("distinct paths must not share a cache directory, both produced %q", a)
	}
	if cacheKey(vcs.Repo{Path: "group/sub/repo"}) != a {
		t.Error("cacheKey must be deterministic")
	}
}

func TestEngineRetargetedDestinationInvalidatesState(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	ctx := context.Background()

	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, ok := eng.opts.State.Get(m.Name, "acme/app"); !ok {
		t.Fatal("seed run should have recorded state")
	}

	// Simulate a name_template edit or destination change: the state still
	// says "app" but the template now renders "renamed".
	names, err := vcs.ParseNameTemplate("renamed")
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	m.Names = names

	rep, err := eng.Run(ctx, []Mirror{m})
	if err != nil {
		t.Fatalf("retarget run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if len(rep.Mirrored) != 1 {
		t.Fatalf("a retargeted destination must not be skipped, got %+v", rep.Mirrored)
	}
	if rs, ok := eng.opts.State.Get(m.Name, "acme/app"); !ok || rs.DestPath != "renamed" {
		t.Errorf("state should now point at the new destination, got %+v", rs)
	}
}

func TestEngineConcurrentReposDoNotRaceOnState(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	// The fixture mirror has one repo. Add more so the pool actually runs
	// several goroutines against shared state.
	srcBase := t.TempDir()
	var repos []vcs.Repo
	for _, name := range []string{"one", "two", "three", "four"} {
		bare := filepath.Join(srcBase, "acme", name+".git")
		work := filepath.Join(t.TempDir(), name)
		git(t, srcBase, "init", "--bare", "--initial-branch=main", bare)
		git(t, filepath.Dir(work), "init", "--initial-branch=main", work)
		if err := testWriteFile(filepath.Join(work, "README.md"), "hi\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
		git(t, work, "add", ".")
		git(t, work, "commit", "-m", "initial")
		git(t, work, "remote", "add", "origin", bare)
		git(t, work, "push", "origin", "main")

		repos = append(repos, vcs.Repo{Name: name, Owner: "acme", Path: "acme/" + name, DefaultBranch: "main", CloneURL: bare})
	}

	m.Source = &fakeLister{repos: repos}
	m.SourceRemote = &fakeRemote{base: srcBase, nesting: true}
	eng.opts.Concurrency = 4

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if len(rep.Mirrored) != 4 {
		t.Fatalf("got %d mirrored, want 4", len(rep.Mirrored))
	}
	for _, r := range repos {
		if _, ok := eng.opts.State.Get(m.Name, r.Path); !ok {
			t.Errorf("state missing entry for %s", r.Path)
		}
	}
}

// fakePRLister returns a fixed pull request list, or an error.
type fakePRLister struct {
	prs []vcs.PullRequest
	err error
}

func (f *fakePRLister) ListPullRequests(context.Context, string, vcs.PRListOptions) ([]vcs.PullRequest, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.prs, nil
}

// enablePRs turns pull request mirroring on for a fixture mirror.
func enablePRs(m *Mirror, lister vcs.PullRequestLister) {
	m.SourcePRs = lister
	m.PullRequests = PRSyncConfig{Enabled: true, BranchPrefix: "syncerd/pr"}
}

// destRef reads a ref from the destination bare repository the fixture
// mirrors into, returning the empty string when the ref does not exist.
func destRef(t *testing.T, m Mirror, ref string) string {
	t.Helper()
	dir := m.Dest.CloneURL("app")
	out, err := NewRunner(nil).run(context.Background(), dir, nil, "rev-parse", "--verify", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func TestForkPullRequestHeadBecomesADestinationBranch(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	fork := newForkRepo(t, "feature", "one")
	forkSHA := strings.TrimSpace(git(t, fork, "rev-parse", "refs/heads/feature"))
	enablePRs(&m, &fakePRLister{prs: []vcs.PullRequest{{
		Number: 7, State: vcs.PROpen, HeadBranch: "feature", HeadSHA: forkSHA,
		HeadRepoCloneURL: fork,
	}}})

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/7"); got != forkSHA {
		t.Errorf("destination refs/heads/syncerd/pr/7 = %q, want %q", got, forkSHA)
	}
	if rep.Mirrored[0].PRBranchesPushed != 1 {
		t.Errorf("PRBranchesPushed = %d, want 1", rep.Mirrored[0].PRBranchesPushed)
	}
}

func TestSameRepositoryPullRequestGetsNoSyntheticBranch(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	enablePRs(&m, &fakePRLister{prs: []vcs.PullRequest{{
		Number: 8, State: vcs.PROpen, HeadBranch: "main", HeadSHA: "irrelevant",
	}}})

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/8"); got != "" {
		t.Errorf("a head living in the source repository must not get a second copy, got %q", got)
	}
	if rep.Mirrored[0].PRBranchesPushed != 0 {
		t.Errorf("PRBranchesPushed = %d, want 0", rep.Mirrored[0].PRBranchesPushed)
	}
}

func TestNewPullRequestDefeatsTheUnchangedSkip(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	ctx := context.Background()

	lister := &fakePRLister{}
	enablePRs(&m, lister)

	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// No branch or tag changes at the source, only a new pull request
	// opened from a fork.
	fork := newForkRepo(t, "feature", "one")
	forkSHA := strings.TrimSpace(git(t, fork, "rev-parse", "refs/heads/feature"))
	lister.prs = []vcs.PullRequest{{
		Number: 7, State: vcs.PROpen, HeadBranch: "feature", HeadSHA: forkSHA,
		HeadRepoCloneURL: fork,
	}}

	rep, err := eng.Run(ctx, []Mirror{m})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if rep.Skipped != 0 {
		t.Fatalf("a repository with a new pull request must not be skipped, Skipped = %d", rep.Skipped)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/7"); got != forkSHA {
		t.Errorf("destination refs/heads/syncerd/pr/7 = %q, want %q", got, forkSHA)
	}
}

func TestClosedPullRequestBranchIsPrunedFromTheDestination(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	ctx := context.Background()

	fork := newForkRepo(t, "feature", "one")
	forkSHA := strings.TrimSpace(git(t, fork, "rev-parse", "refs/heads/feature"))
	lister := &fakePRLister{prs: []vcs.PullRequest{{
		Number: 7, State: vcs.PROpen, HeadBranch: "feature", HeadSHA: forkSHA,
		HeadRepoCloneURL: fork,
	}}}
	enablePRs(&m, lister)

	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/7"); got == "" {
		t.Fatal("the branch should exist after the first run")
	}

	// The pull request closes, so the source stops listing it.
	lister.prs = nil

	if _, err := eng.Run(ctx, []Mirror{m}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/7"); got != "" {
		t.Errorf("a closed pull request's branch must be pruned, still at %q", got)
	}
}

func TestSourceBranchUnderThePrefixFailsTheRepository(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	source := m.SourceRemote.CloneURL("acme/app")
	sha := strings.TrimSpace(git(t, source, "rev-parse", "refs/heads/main"))
	git(t, source, "update-ref", "refs/heads/syncerd/pr/1", sha)

	enablePRs(&m, &fakePRLister{})

	rep, _ := eng.Run(context.Background(), []Mirror{m})
	if len(rep.Failures) != 1 {
		t.Fatalf("got %d failures, want 1: %+v", len(rep.Failures), rep.Failures)
	}
	if rep.Failures[0].Stage != "pull-requests" {
		t.Errorf("Stage = %q, want pull-requests", rep.Failures[0].Stage)
	}
	if !strings.Contains(rep.Failures[0].Error, "branch_prefix") {
		t.Errorf("the error should name branch_prefix, got %q", rep.Failures[0].Error)
	}
}

func TestUnreachableForkHeadWarnsAndContinues(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	enablePRs(&m, &fakePRLister{prs: []vcs.PullRequest{{
		Number: 7, State: vcs.PROpen, HeadBranch: "feature", HeadSHA: "aaa",
		HeadRepoCloneURL: filepath.Join(t.TempDir(), "deleted-fork.git"),
	}}})

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("one unreachable fork must not fail the run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if len(rep.Mirrored) != 1 {
		t.Fatalf("the repository's branches should still mirror: %+v", rep.Mirrored)
	}
	if rep.Mirrored[0].PRBranchesPushed != 0 {
		t.Errorf("PRBranchesPushed = %d, want 0", rep.Mirrored[0].PRBranchesPushed)
	}
}

func TestPullRequestListingFailureFailsAtThePullRequestsStage(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	enablePRs(&m, &fakePRLister{err: errors.New("github: 403 rate limited")})

	rep, _ := eng.Run(context.Background(), []Mirror{m})
	if len(rep.Failures) != 1 {
		t.Fatalf("got %d failures, want 1: %+v", len(rep.Failures), rep.Failures)
	}
	if rep.Failures[0].Stage != "pull-requests" {
		t.Errorf("Stage = %q, want pull-requests", rep.Failures[0].Stage)
	}
	if len(rep.Mirrored) != 0 {
		t.Errorf("a failed listing must not report the repository as mirrored: %+v", rep.Mirrored)
	}
}

func TestPullRequestsDisabledMakesNoListingCall(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	// SourcePRs is wired but the block is off, which is what BuildMirrors
	// produces for a mirror with no pull_requests configured.
	m.SourcePRs = &fakePRLister{err: errors.New("must not be called")}

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("a disabled block must not list pull requests: %+v", rep.Failures)
	}
}

func TestMirrorObjectsGivesEverySourcePullRequestABranch(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	// A pull request whose head is a branch of the source repository. With
	// mirror_objects on it gets a syncerd/pr branch too, so a destination
	// pull request has one uniform head name to point at.
	source := m.SourceRemote.CloneURL("acme/app")
	sha := strings.TrimSpace(git(t, source, "rev-parse", "refs/heads/main"))

	enablePRs(&m, &fakePRLister{prs: []vcs.PullRequest{{
		Number: 8, State: vcs.PROpen, HeadBranch: "main", HeadSHA: sha,
	}}})
	m.PullRequests.MirrorObjects = true

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/8"); got != sha {
		t.Errorf("destination refs/heads/syncerd/pr/8 = %q, want %q", got, sha)
	}
	if rep.Mirrored[0].PRBranchesPushed != 1 {
		t.Errorf("PRBranchesPushed = %d, want 1", rep.Mirrored[0].PRBranchesPushed)
	}
}

func TestWithoutMirrorObjectsASameRepoPullRequestStillGetsNoBranch(t *testing.T) {
	eng, m, _ := newEngineFixture(t)

	source := m.SourceRemote.CloneURL("acme/app")
	sha := strings.TrimSpace(git(t, source, "rev-parse", "refs/heads/main"))

	enablePRs(&m, &fakePRLister{prs: []vcs.PullRequest{{
		Number: 8, State: vcs.PROpen, HeadBranch: "main", HeadSHA: sha,
	}}})

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := destRef(t, m, "refs/heads/syncerd/pr/8"); got != "" {
		t.Errorf("P1 behaviour must be unchanged when mirror_objects is off, got %q", got)
	}
	if rep.Mirrored[0].PRBranchesPushed != 0 {
		t.Errorf("PRBranchesPushed = %d, want 0", rep.Mirrored[0].PRBranchesPushed)
	}
}
