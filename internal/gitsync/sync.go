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
	Stage      string // discover | ensure | fetch | push | adopt
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
}

// Engine mirrors repositories for a set of mirrors.
type Engine struct {
	opts EngineOptions

	mu     sync.Mutex
	report *GitReport
}

// NewEngine returns an engine. Concurrency below one is treated as one.
func NewEngine(opts EngineOptions) *Engine {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Runner == nil {
		opts.Runner = NewRunner(nil)
	}
	if opts.State == nil {
		opts.State = state.NewGit()
	}
	return &Engine{opts: opts}
}

// Run mirrors every repository across every mirror. A single repository
// failing does not abort the run unless FailFast is set.
func (e *Engine) Run(ctx context.Context, mirrors []Mirror) (*GitReport, error) {
	rep := &GitReport{StartedAt: time.Now().UTC()}
	e.report = rep

	if err := e.opts.Runner.Preflight(ctx); err != nil {
		rep.EndedAt = time.Now().UTC()
		return rep, err
	}

	for _, m := range mirrors {
		repos, err := m.Source.ListRepos(ctx)
		if err != nil {
			e.addFailure(GitFailure{Mirror: m.Name, Stage: "discover", Error: err.Error()})
			if e.opts.FailFast {
				rep.EndedAt = time.Now().UTC()
				return rep, err
			}
			continue
		}

		selected := vcs.Apply(m.Filter, repos)
		log.Printf("mirror %s: %d repositories discovered, %d selected", m.Name, len(repos), len(selected))

		if err := e.runMirror(ctx, m, selected); err != nil {
			rep.EndedAt = time.Now().UTC()
			return rep, err
		}
	}

	rep.EndedAt = time.Now().UTC()

	if !e.opts.DryRun {
		if err := e.opts.State.Save(e.opts.StatePath); err != nil {
			return rep, fmt.Errorf("failed to save git state: %w", err)
		}
	}

	if len(rep.Failures) > 0 {
		return rep, fmt.Errorf("git sync completed with %d failures", len(rep.Failures))
	}
	return rep, nil
}

// runMirror processes one mirror's repositories through a bounded pool.
func (e *Engine) runMirror(ctx context.Context, m Mirror, repos []vcs.Repo) error {
	sem := make(chan struct{}, e.opts.Concurrency)
	var wg sync.WaitGroup

	for _, r := range repos {
		wg.Add(1)
		go func(repo vcs.Repo) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					e.addFailure(GitFailure{
						Mirror:     m.Name,
						SourceRepo: repo.Path,
						Stage:      "push",
						Error:      fmt.Sprintf("panic: %v", rec),
					})
				}
			}()

			sem <- struct{}{}
			defer func() { <-sem }()

			e.mirrorRepo(ctx, m, repo)
		}(r)
	}

	wg.Wait()
	return nil
}

// mirrorRepo mirrors a single repository, recording the outcome.
func (e *Engine) mirrorRepo(ctx context.Context, m Mirror, repo vcs.Repo) {
	destName, err := m.Names.Render(repo)
	if err != nil {
		e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "ensure", Error: err.Error()})
		return
	}
	if strings.Contains(destName, "/") && !m.Dest.SupportsNesting() {
		e.addFailure(GitFailure{
			Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "ensure",
			Error: fmt.Sprintf("destination does not support nested repository paths but name_template rendered %q", destName),
		})
		return
	}

	srcCred, err := m.SourceRemote.GitCredential(ctx)
	if err != nil {
		e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "fetch", Error: err.Error()})
		return
	}
	srcURL := m.SourceRemote.CloneURL(repo.Path)

	refs, err := e.opts.Runner.LsRemote(ctx, srcURL, srcCred)
	if err != nil {
		e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, Stage: "fetch", Error: err.Error()})
		return
	}
	if len(refs) == 0 {
		log.Printf("mirror %s: %s is empty, skipping", m.Name, repo.Path)
		e.addSkip()
		return
	}

	fp := Fingerprint(refs)
	if prev, ok := e.opts.State.Get(m.Name, repo.Path); ok && prev.Fingerprint == fp {
		log.Printf("mirror %s: %s unchanged, skipping", m.Name, repo.Path)
		e.addSkip()
		return
	}

	dstCred, err := m.Dest.GitCredential(ctx)
	if err != nil {
		e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "ensure", Error: err.Error()})
		return
	}
	destURL := m.Dest.CloneURL(destName)

	// Adopt guard: refuse a first run against a destination that already
	// has refs, unless the mirror opts in.
	_, hasState := e.opts.State.Get(m.Name, repo.Path)
	if !hasState && !m.Adopt {
		existing, lsErr := e.opts.Runner.LsRemote(ctx, destURL, dstCred)
		if lsErr == nil && len(existing) > 0 {
			e.addFailure(GitFailure{
				Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "adopt",
				Error: fmt.Sprintf("destination already has %d refs and this mirror has no prior state; set adopt: true on mirror %q to overwrite it", len(existing), m.Name),
			})
			return
		}
	}

	created := false
	if m.CreateMissing && m.DestEnsurer != nil {
		existing, lsErr := e.opts.Runner.LsRemote(ctx, destURL, dstCred)
		if lsErr != nil || len(existing) == 0 {
			if _, err := m.DestEnsurer.EnsureRepo(ctx, vcs.RepoSpec{
				Path:          destName,
				DefaultBranch: repo.DefaultBranch,
				Visibility:    m.Visibility,
			}); err != nil {
				e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "ensure", Error: err.Error()})
				return
			}
			created = true
		}
	}

	cacheDir := filepath.Join(e.opts.WorkDir, m.Name, cacheKey(repo)+".git")
	if err := e.opts.Runner.SyncCache(ctx, srcURL, cacheDir, srcCred); err != nil {
		e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "fetch", Error: err.Error()})
		return
	}

	res, err := e.opts.Runner.Push(ctx, cacheDir, destURL, dstCred, m.PushMode, e.opts.DryRun)
	if err != nil {
		e.addFailure(GitFailure{Mirror: m.Name, SourceRepo: repo.Path, DestRepo: destName, Stage: "push", Error: err.Error()})
		return
	}

	if !e.opts.DryRun {
		e.opts.State.Mark(m.Name, repo.Path, destName, fp)

		if setter, ok := m.Dest.(vcs.DefaultBranchSetter); ok && repo.DefaultBranch != "" && created {
			if err := setter.SetDefaultBranch(ctx, destName, repo.DefaultBranch); err != nil {
				log.Printf("mirror %s: could not set default branch on %s: %v", m.Name, destName, err)
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
