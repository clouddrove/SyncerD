package gitsync

import (
	"context"
	"path/filepath"
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

// fakeEnsurer records creations and initialises a bare repo on disk.
type fakeEnsurer struct {
	base    string
	created []string
	t       *testing.T
}

func (f *fakeEnsurer) EnsureRepo(_ context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	dir := filepath.Join(f.base, spec.Path+".git")
	git(f.t, f.base, "init", "--bare", "--initial-branch=main", dir)
	f.created = append(f.created, spec.Path)
	return vcs.Repo{Name: spec.Path, Path: spec.Path}, nil
}

func newEngineFixture(t *testing.T) (*Engine, Mirror, *fakeEnsurer) {
	t.Helper()

	srcBase := t.TempDir()
	dstBase := t.TempDir()

	// A source repo named "app" under owner "acme", with one commit and a
	// tag. The bare repo lives at the nested path matching Repo.Path
	// ("acme/app"), since that is what the engine passes to
	// SourceRemote.CloneURL when building the source clone URL.
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

	m := Mirror{
		Name:          "fake",
		Source:        &fakeLister{repos: []vcs.Repo{{Name: "app", Owner: "acme", Path: "acme/app", DefaultBranch: "main"}}},
		SourceRemote:  &fakeRemote{base: srcBase, nesting: true},
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

func TestEngineDryRunWritesNoState(t *testing.T) {
	eng, m, _ := newEngineFixture(t)
	eng.opts.DryRun = true

	rep, err := eng.Run(context.Background(), []Mirror{m})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}
	if _, ok := eng.opts.State.Get("fake", "acme/app"); ok {
		t.Error("dry run must not record state")
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

func TestEngineAdoptAllowsWhenOptedIn(t *testing.T) {
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
		t.Errorf("adopt true must bypass the guard: %+v", rep.Failures)
	}
}
