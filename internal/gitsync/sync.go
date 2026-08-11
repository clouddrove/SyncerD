package gitsync

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
)

// MirrorEvent records one repository successfully mirrored.
type MirrorEvent struct {
	Mirror      string
	SourceRepo  string
	DestRepo    string
	RefsPushed  int
	RefsDeleted int
	Created     bool
}

// GitFailure records one repository that failed, and where.
type GitFailure struct {
	Mirror     string
	SourceRepo string
	DestRepo   string
	Stage      string // discover | ensure | fetch | push | adopt | worker
	Error      string
}

// GitReport summarises one git-sync run.
type GitReport struct {
	StartedAt time.Time
	EndedAt   time.Time
	Mirrored  []MirrorEvent
	Skipped   int
	Failures  []GitFailure
}

// Mirror is one resolved source to destination pair, ready to run.
type Mirror struct {
	Name          string
	Source        vcs.Lister
	SourceRemote  vcs.Remote
	Dest          vcs.Remote
	DestEnsurer   vcs.Ensurer
	Filter        vcs.Filter
	Names         *vcs.NameTemplate
	PushMode      PushMode
	Adopt         bool
	CreateMissing bool
	Visibility    string
}

// EngineOptions configures a run.
type EngineOptions struct {
	WorkDir     string
	Concurrency int
	DryRun      bool
	FailFast    bool
	State       *state.GitState
	StatePath   string
	Runner      *Runner
	Redactor    *Redactor
}

// Engine mirrors repositories for a set of mirrors.
type Engine struct {
	opts EngineOptions

	mu       sync.Mutex
	report   *GitReport
	firstErr error
}

// NewEngine returns an engine. Concurrency below one is treated as one.
func NewEngine(opts EngineOptions) *Engine {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Runner == nil {
		opts.Runner = NewRunner(opts.Redactor)
	}
	if opts.State == nil {
		opts.State = state.NewGit()
	}
	return &Engine{opts: opts}
}

// Run mirrors every repository across every mirror. A single repository
// failing does not abort the run unless FailFast is set.
//
// Run is not safe to call concurrently on the same Engine, because it
// stores per run state on the receiver.
func (e *Engine) Run(ctx context.Context, mirrors []Mirror) (*GitReport, error) {
	rep := &GitReport{StartedAt: time.Now().UTC()}
	e.report = rep
	e.firstErr = nil

	if err := e.opts.Runner.Preflight(ctx); err != nil {
		rep.EndedAt = time.Now().UTC()
		return rep, err
	}

	// The lock guards WorkDir, the clone cache directory, against a second
	// SyncerD process running concurrently against it. It is taken even on
	// a dry run: SyncCache below still populates the cache during a dry
	// run, because "git push --dry-run" needs the objects locally to
	// report what it would send. Only an empty WorkDir (no cache in use)
	// skips the lock.
	if e.opts.WorkDir != "" {
		lock, err := AcquireLock(e.opts.WorkDir)
		if err != nil {
			rep.EndedAt = time.Now().UTC()
			return rep, err
		}
		defer func() { _ = lock.Release() }()
	}

	// save persists state on every exit path below, not just the happy
	// path, so a FailFast abort does not discard the fingerprint of
	// repositories this run already mirrored before it stopped.
	save := func() error {
		if e.opts.DryRun {
			return nil
		}
		if err := e.opts.State.Save(e.opts.StatePath); err != nil {
			return fmt.Errorf("failed to save git state: %w", err)
		}
		return nil
	}

	for _, m := range mirrors {
		if ctx.Err() != nil {
			break
		}

		repos, err := m.Source.ListRepos(ctx)
		if err != nil {
			e.addFailure(GitFailure{Mirror: m.Name, Stage: "discover", Error: e.redact(err.Error())})
			if e.opts.FailFast {
				rep.EndedAt = time.Now().UTC()
				if serr := save(); serr != nil {
					return rep, serr
				}
				return rep, err
			}
			continue
		}

		selected := vcs.Apply(m.Filter, repos)
		log.Printf("mirror %s: %d repositories discovered, %d selected", m.Name, len(repos), len(selected))

		if err := e.runMirror(ctx, m, selected); err != nil {
			rep.EndedAt = time.Now().UTC()
			if serr := save(); serr != nil {
				return rep, serr
			}
			return rep, err
		}
	}

	rep.EndedAt = time.Now().UTC()

	if err := save(); err != nil {
		return rep, err
	}

	if len(rep.Failures) > 0 {
		return rep, fmt.Errorf("git sync completed with %d failures", len(rep.Failures))
	}
	return rep, nil
}

// runMirror processes one mirror's repositories through a bounded pool.
// Under FailFast the first failure cancels the remaining work.
func (e *Engine) runMirror(ctx context.Context, m Mirror, repos []vcs.Repo) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, e.opts.Concurrency)
	var wg sync.WaitGroup

	for _, r := range repos {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(repo vcs.Repo) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					e.fail(cancel, GitFailure{
						Mirror:     m.Name,
						SourceRepo: repo.Path,
						Stage:      "worker",
						Error:      fmt.Sprintf("panic: %v", rec),
					})
				}
			}()

			sem <- struct{}{}
			defer func() { <-sem }()

			// A run cancelled while this goroutine waited for a slot has
			// nothing useful left to do.
			if ctx.Err() != nil {
				return
			}
			e.mirrorRepo(ctx, cancel, m, repo)
		}(r)
	}

	wg.Wait()
	return e.takenErr()
}

// mirrorRepo mirrors a single repository, recording the outcome. Under
// FailFast, cancel aborts the remaining pool once this call reports a
// failure.
func (e *Engine) mirrorRepo(ctx context.Context, cancel context.CancelFunc, m Mirror, repo vcs.Repo) {
	destName, err := m.Names.Render(repo)
	if err != nil {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "ensure", Error: e.redact(err.Error())})
		return
	}
	if strings.Contains(destName, "/") && !m.Dest.SupportsNesting() {
		e.fail(cancel, GitFailure{
			Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "ensure",
			Error: fmt.Sprintf("destination does not support nested repository paths but name_template rendered %q", destName),
		})
		return
	}

	srcCred, err := m.SourceRemote.GitCredential(ctx)
	if err != nil {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "fetch", Error: e.redact(err.Error())})
		return
	}
	srcURL := repo.CloneURL
	if srcURL == "" {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "fetch",
			Error: "source provider did not report a clone URL for this repository"})
		return
	}

	refs, err := e.opts.Runner.LsRemote(ctx, srcURL, srcCred)
	if err != nil {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "fetch", Error: e.redact(err.Error())})
		return
	}
	if len(refs) == 0 {
		log.Printf("mirror %s: %s is empty, skipping", m.Name, repo.Path)
		e.addSkip()
		return
	}

	fp := Fingerprint(refs)
	if prev, ok := e.stateGet(m.Name, repo.Path); ok && prev.Fingerprint == fp {
		log.Printf("mirror %s: %s unchanged, skipping", m.Name, repo.Path)
		e.addSkip()
		return
	}

	dstCred, err := m.Dest.GitCredential(ctx)
	if err != nil {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "ensure", Error: e.redact(err.Error())})
		return
	}
	destURL := m.Dest.CloneURL(destName)

	// Inspect the destination once. A successful listing means the
	// repository exists, whether or not it has refs. An error means we
	// cannot tell an absent repository from an unreachable one, so the
	// only safe reading is that we do not know what is there.
	destRefs, destErr := e.opts.Runner.LsRemote(ctx, destURL, dstCred)
	_, hasState := e.stateGet(m.Name, repo.Path)

	needsCreate := false
	if destErr != nil {
		if !m.CreateMissing || m.DestEnsurer == nil {
			// Cannot inspect and cannot create. Refusing here keeps a
			// pruning push from running against a destination whose
			// contents are unknown.
			e.fail(cancel, GitFailure{
				Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "adopt",
				Error: fmt.Sprintf("cannot inspect destination and create_missing is off, refusing to push: %s", e.redact(destErr.Error())),
			})
			return
		}
		needsCreate = true
	}

	// The adopt guard. A destination that already holds refs, with no
	// prior state for this mirror, is refused unless the mirror opts in.
	if !hasState && !m.Adopt && len(destRefs) > 0 {
		e.fail(cancel, GitFailure{
			Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "adopt",
			Error: fmt.Sprintf("destination already has %d refs and this mirror has no prior state; set adopt: true on mirror %q to overwrite it", len(destRefs), m.Name),
		})
		return
	}

	created := false
	if needsCreate {
		if e.opts.DryRun {
			// A dry run reports the creation without performing it.
			created = true
			log.Printf("mirror %s: would create destination %s", m.Name, destName)
		} else {
			if _, err := m.DestEnsurer.EnsureRepo(ctx, vcs.RepoSpec{
				Path:          destName,
				DefaultBranch: repo.DefaultBranch,
				Visibility:    m.Visibility,
			}); err != nil {
				e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "ensure", Error: e.redact(err.Error())})
				return
			}
			created = true

			// Re-inspect now that the repository should exist. If it is
			// still unreachable, refuse rather than push blind.
			destRefs, destErr = e.opts.Runner.LsRemote(ctx, destURL, dstCred)
			if destErr != nil {
				e.fail(cancel, GitFailure{
					Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "adopt",
					Error: fmt.Sprintf("destination still unreachable after creation, refusing to push: %s", e.redact(destErr.Error())),
				})
				return
			}
			if !hasState && !m.Adopt && len(destRefs) > 0 {
				e.fail(cancel, GitFailure{
					Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "adopt",
					Error: fmt.Sprintf("destination already has %d refs and this mirror has no prior state; set adopt: true on mirror %q to overwrite it", len(destRefs), m.Name),
				})
				return
			}
		}
	}

	cacheDir := filepath.Join(e.opts.WorkDir, m.Name, cacheKey(repo)+".git")
	// The cache is populated even on a dry run, because git push --dry-run
	// needs the objects locally to report what it would send. This writes
	// only inside WorkDir, never to the destination.
	if err := e.opts.Runner.SyncCache(ctx, srcURL, cacheDir, srcCred); err != nil {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "fetch", Error: e.redact(err.Error())})
		return
	}

	if e.opts.DryRun && needsCreate {
		// The destination does not exist and a dry run must not create
		// it (that is the whole point of a dry run), so there is
		// nothing at destURL for git push --dry-run to negotiate
		// against; git would only fail with "repository not found".
		// Report the refs a real run would push instead of attempting
		// a push that cannot succeed.
		e.addEvent(MirrorEvent{
			Mirror:     m.Name,
			SourceRepo: repo.Path,
			DestRepo:   destName,
			RefsPushed: len(refs),
			Created:    created,
		})
		log.Printf("mirror %s: %s -> %s (dry run, destination does not exist yet; %d refs would be pushed)", m.Name, repo.Path, destName, len(refs))
		return
	}

	res, err := e.opts.Runner.Push(ctx, cacheDir, destURL, dstCred, m.PushMode, e.opts.DryRun)
	if err != nil {
		e.fail(cancel, GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "push", Error: e.redact(err.Error())})
		return
	}

	if !e.opts.DryRun {
		e.stateMark(m.Name, repo.Path, destName, fp)

		if setter, ok := m.Dest.(vcs.DefaultBranchSetter); ok && repo.DefaultBranch != "" && created {
			if err := setter.SetDefaultBranch(ctx, destName, repo.DefaultBranch); err != nil {
				log.Printf("mirror %s: could not set default branch on %s: %s", m.Name, destName, e.redact(err.Error()))
			}
		}
	}

	e.addEvent(MirrorEvent{
		Mirror:      m.Name,
		SourceRepo:  repo.Path,
		DestRepo:    destName,
		RefsPushed:  res.Pushed,
		RefsDeleted: res.Deleted,
		Created:     created,
	})
	log.Printf("mirror %s: %s -> %s (%d pushed, %d deleted)", m.Name, repo.Path, destName, res.Pushed, res.Deleted)
}

// cacheKey turns a repository path into a flat directory name.
func cacheKey(r vcs.Repo) string {
	return strings.ReplaceAll(r.Path, "/", "__")
}

// redact strips configured secrets from any string bound for a failure
// record, a log line, or a Slack message.
func (e *Engine) redact(s string) string {
	return e.opts.Redactor.Redact(s)
}

// fail records a failure and, under FailFast, remembers the first error
// and cancels the run.
func (e *Engine) fail(cancel context.CancelFunc, f GitFailure) {
	e.addFailure(f)
	if !e.opts.FailFast {
		return
	}
	e.mu.Lock()
	if e.firstErr == nil {
		e.firstErr = fmt.Errorf("mirror %s: %s failed at stage %s: %s", f.Mirror, f.SourceRepo, f.Stage, f.Error)
	}
	e.mu.Unlock()
	cancel()
}

// takenErr returns the first recorded FailFast error, if any.
func (e *Engine) takenErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.firstErr
}

// stateGet reads mirror state under the engine lock, because the pool
// touches it from several goroutines and GitState has no locking of its
// own.
func (e *Engine) stateGet(mirror, repoPath string) (state.RepoState, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opts.State.Get(mirror, repoPath)
}

// stateMark records a successful mirror under the engine lock.
func (e *Engine) stateMark(mirror, repoPath, destPath, fingerprint string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opts.State.Mark(mirror, repoPath, destPath, fingerprint)
}

func (e *Engine) addEvent(ev MirrorEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.report.Mirrored = append(e.report.Mirrored, ev)
}

func (e *Engine) addFailure(f GitFailure) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.report.Failures = append(e.report.Failures, f)
}

func (e *Engine) addSkip() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.report.Skipped++
}
