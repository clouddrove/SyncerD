package gitsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/config"
)

func buildableConfig() *config.GitConfig {
	g := &config.GitConfig{
		Providers: []config.GitProviderConfig{
			{Name: "gh", Type: "github", Owner: "acme", Token: "ghp_token_value"},
			{Name: "gl", Type: "gitlab", Owner: "mirrors", Token: "glpat_token_value"},
		},
		Mirrors: []config.MirrorConfig{
			{
				Name:        "gh-to-gl",
				Source:      "gh",
				Destination: "gl",
				Filters: config.FilterConfig{
					Include: []string{"svc-*"},
					Exclude: []string{"svc-legacy"},
				},
			},
		},
	}
	g.ApplyDefaults()
	return g
}

func TestBuildMirrors(t *testing.T) {
	mirrors, redactor, err := BuildMirrors(buildableConfig())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(mirrors) != 1 {
		t.Fatalf("got %d mirrors, want 1", len(mirrors))
	}

	m := mirrors[0]
	if m.Name != "gh-to-gl" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Source == nil || m.SourceRemote == nil || m.Dest == nil || m.DestEnsurer == nil {
		t.Errorf("mirror is missing a role: %+v", m)
	}
	if m.Names == nil {
		t.Error("Names is nil, want a parsed name template")
	}
	if m.PushMode != PushMirror {
		t.Errorf("PushMode = %q", m.PushMode)
	}
	if !m.Filter.SkipArchived || !m.Filter.SkipForks {
		t.Errorf("filter defaults not applied: %+v", m.Filter)
	}
	if len(m.Filter.Include) != 1 || m.Filter.Include[0] != "svc-*" {
		t.Errorf("Filter.Include = %v, want [svc-*]", m.Filter.Include)
	}
	if len(m.Filter.Exclude) != 1 || m.Filter.Exclude[0] != "svc-legacy" {
		t.Errorf("Filter.Exclude = %v, want [svc-legacy]", m.Filter.Exclude)
	}
	if !m.CreateMissing {
		t.Error("CreateMissing = false, want the default of true")
	}
	if m.Visibility != "private" {
		t.Errorf("Visibility = %q, want private", m.Visibility)
	}
	if m.Adopt {
		t.Error("Adopt = true, want the default of false")
	}

	if got := redactor.Redact("token is ghp_token_value"); strings.Contains(got, "ghp_token_value") {
		t.Errorf("redactor missing provider token: %q", got)
	}
}

func TestBuildMirrorsRejectsUnimplementedProvider(t *testing.T) {
	g := buildableConfig()
	g.Providers = append(g.Providers, config.GitProviderConfig{
		Name: "cc", Type: "codecommit", Region: "us-east-1",
	})
	g.Mirrors = append(g.Mirrors, config.MirrorConfig{Name: "gh-to-cc", Source: "gh", Destination: "cc"})
	g.ApplyDefaults()

	_, _, err := BuildMirrors(g)
	if err == nil {
		t.Fatal("expected an error for a provider type with no implementation yet")
	}
	if !strings.Contains(err.Error(), "codecommit") {
		t.Errorf("error should name the type, got %v", err)
	}
}

func TestBuildMirrorsCarriesPushMode(t *testing.T) {
	g := buildableConfig()
	g.Mirrors[0].PushMode = "additive"
	g.ApplyDefaults()

	mirrors, _, err := BuildMirrors(g)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if mirrors[0].PushMode != PushAdditive {
		t.Errorf("PushMode = %q, want additive", mirrors[0].PushMode)
	}
}

func TestBuildMirrorsRejectsNestedNameForFlatDestination(t *testing.T) {
	g := buildableConfig()
	// GitHub repository names cannot contain a slash, so a template that
	// renders one must be refused at build time rather than at push time.
	g.Mirrors[0].Source = "gl"
	g.Mirrors[0].Destination = "gh"
	g.Mirrors[0].NameTemplate = "{{ .Owner }}/{{ .Repo }}"
	g.ApplyDefaults()

	_, _, err := BuildMirrors(g)
	if err == nil {
		t.Fatal("a nested name template against a GitHub destination must be rejected")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("error should explain the nesting problem, got %v", err)
	}
}

// TestBuildMirrorsRejectsDestinationThatCannotCreate is deliberately not
// implemented: both providers BuildMirrors can construct today, github and
// gitlab, satisfy vcs.Ensurer, so there is no configuration BuildMirrors can
// be given that reaches the "cannot create repositories" branch without a
// fake provider. BuildMirrors takes config, not constructed providers, so
// forcing the path would mean inventing a provider type that does not exist
// in the registry, which contradicts what the branch actually guards. Once a
// provider without EnsureRepo lands, this path becomes reachable and should
// be covered then.

func TestBuildMirrorsUnreferencedProviderNotBuilt(t *testing.T) {
	g := buildableConfig()
	// A provider of an unimplemented type that no mirror references must
	// not block startup: BuildMirrors only constructs providers a mirror
	// actually names.
	g.Providers = append(g.Providers, config.GitProviderConfig{
		Name: "unused-cc", Type: "codecommit", Region: "us-east-1",
	})
	g.ApplyDefaults()

	mirrors, _, err := BuildMirrors(g)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(mirrors) != 1 {
		t.Fatalf("got %d mirrors, want 1", len(mirrors))
	}
}

func TestNewSyncerWiresRedactorAndState(t *testing.T) {
	cfg := &config.Config{Git: buildableConfig()}
	cfg.Git.StatePath = filepath.Join(t.TempDir(), "git-state.json")
	cfg.Git.WorkDir = t.TempDir()

	s, err := NewSyncer(cfg, false)
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if s.engine.opts.Redactor == nil {
		t.Error("the engine must receive a redactor, or provider errors reach logs and Slack with tokens intact")
	}
	if s.engine.opts.Runner == nil {
		t.Error("the engine must receive a runner built with the same redactor")
	}
	if s.engine.opts.StatePath != cfg.Git.StatePath {
		t.Errorf("StatePath = %q, want the git state path %q, not the image sync one", s.engine.opts.StatePath, cfg.Git.StatePath)
	}
	if s.engine.opts.WorkDir != cfg.Git.WorkDir {
		t.Errorf("WorkDir = %q, want %q", s.engine.opts.WorkDir, cfg.Git.WorkDir)
	}
	if s.engine.opts.Concurrency != cfg.Git.Concurrency {
		t.Errorf("Concurrency = %d, want %d", s.engine.opts.Concurrency, cfg.Git.Concurrency)
	}
}

func TestNewSyncerRedactorCarriesProviderSecrets(t *testing.T) {
	cfg := &config.Config{Git: buildableConfig()}
	cfg.Git.StatePath = filepath.Join(t.TempDir(), "git-state.json")

	s, err := NewSyncer(cfg, false)
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	got := s.engine.opts.Redactor.Redact("failed with ghp_token_value")
	if strings.Contains(got, "ghp_token_value") {
		t.Errorf("the engine redactor must know the provider tokens, got %q", got)
	}
}

func TestNewSyncerRecordsDryRun(t *testing.T) {
	cfg := &config.Config{Git: buildableConfig()}
	cfg.Git.StatePath = filepath.Join(t.TempDir(), "git-state.json")

	s, err := NewSyncer(cfg, true)
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if !s.dryRun {
		t.Error("a dry run syncer must remember it is a dry run, so it does not notify Slack")
	}
	if !s.engine.opts.DryRun {
		t.Error("the engine must be told it is a dry run")
	}
}
