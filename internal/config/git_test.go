package config

import (
	"strings"
	"testing"
)

func validGit() *GitConfig {
	return &GitConfig{
		Providers: []GitProviderConfig{
			{Name: "gh", Type: "github", Owner: "clouddrove", Token: "ghp_token_value"},
			{Name: "gl", Type: "gitlab", Owner: "mirrors", Token: "glpat_token_value"},
		},
		Mirrors: []MirrorConfig{
			{Name: "gh-to-gl", Source: "gh", Destination: "gl"},
		},
	}
}

func TestValidateGitSyncAcceptsValidConfig(t *testing.T) {
	cfg := &Config{Git: validGit()}
	cfg.Git.ApplyDefaults()
	if err := cfg.ValidateGitSync(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestValidateGitSyncRequiresGitSection(t *testing.T) {
	cfg := &Config{}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "git configuration is required") {
		t.Fatalf("expected missing git section error, got %v", err)
	}
}

func TestValidateGitSyncRequiresMirrors(t *testing.T) {
	g := validGit()
	g.Mirrors = nil
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "at least one mirror is required") {
		t.Fatalf("expected mirror error, got %v", err)
	}
}

func TestValidateGitSyncRejectsDuplicateProviderNames(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{Name: "gh", Type: "gitlab", Owner: "x", Token: "y_token_value"})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected duplicate provider error, got %v", err)
	}
}

func TestValidateGitSyncRejectsUnknownProviderReference(t *testing.T) {
	g := validGit()
	g.Mirrors[0].Destination = "nope"
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected unknown provider error, got %v", err)
	}
}

func TestValidateGitSyncRejectsUnknownProviderType(t *testing.T) {
	g := validGit()
	g.Providers[0].Type = "svn"
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "unsupported provider type") {
		t.Fatalf("expected unsupported type error, got %v", err)
	}
}

func TestValidateGitSyncRejectsBadPushMode(t *testing.T) {
	g := validGit()
	g.Mirrors[0].PushMode = "yolo"
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "push_mode") {
		t.Fatalf("expected push mode error, got %v", err)
	}
}

func TestValidateGitSyncRejectsNestedTemplateForFlatDestination(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "cc", Type: "codecommit", Region: "us-east-1",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{
		Name: "gh-to-cc", Source: "gh", Destination: "cc",
		NameTemplate: "{{ .Path }}",
	})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "does not support nested") {
		t.Fatalf("expected nesting error, got %v", err)
	}
}

func TestValidateGitSyncRequiresBitbucketEmail(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "bb", Type: "bitbucket", Owner: "ws", Token: "token_value_here",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-bb", Source: "gh", Destination: "bb"})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "email is required") {
		t.Fatalf("expected bitbucket email error, got %v", err)
	}
}

func TestValidateGitSyncRequiresAzureProject(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "ado", Type: "azuredevops", Owner: "org", Token: "pat_value_here",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-ado", Source: "gh", Destination: "ado"})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "project is required") {
		t.Fatalf("expected azure project error, got %v", err)
	}
}

func TestValidateGitSyncRejectsDuplicateMirrorNames(t *testing.T) {
	g := validGit()
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-gl", Source: "gl", Destination: "gh"})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected duplicate mirror error, got %v", err)
	}
}

func TestApplyDefaults(t *testing.T) {
	g := validGit()
	g.ApplyDefaults()

	if g.WorkDir != "/var/lib/syncerd/git" {
		t.Errorf("WorkDir = %q", g.WorkDir)
	}
	if g.StatePath != ".syncerd-git-state.json" {
		t.Errorf("StatePath = %q", g.StatePath)
	}
	if g.Schedule != "0 */6 * * *" {
		t.Errorf("Schedule = %q", g.Schedule)
	}
	if g.Concurrency != 4 {
		t.Errorf("Concurrency = %d", g.Concurrency)
	}

	m := g.Mirrors[0]
	if !m.CreateMissingOrDefault() {
		t.Error("create_missing should default to true")
	}
	if m.Visibility != "private" {
		t.Errorf("Visibility = %q, want private", m.Visibility)
	}
	if m.PushMode != "mirror" {
		t.Errorf("PushMode = %q, want mirror", m.PushMode)
	}
	if !m.Filters.SkipArchivedOrDefault() {
		t.Error("skip_archived should default to true")
	}
	if !m.Filters.SkipForksOrDefault() {
		t.Error("skip_forks should default to true")
	}
}

func TestApplyDefaultsRespectsExplicitFalse(t *testing.T) {
	g := validGit()
	no := false
	g.Mirrors[0].CreateMissing = &no
	g.Mirrors[0].Filters.SkipArchived = &no
	g.ApplyDefaults()

	if g.Mirrors[0].CreateMissingOrDefault() {
		t.Error("explicit create_missing false must be preserved")
	}
	if g.Mirrors[0].Filters.SkipArchivedOrDefault() {
		t.Error("explicit skip_archived false must be preserved")
	}
}

func TestApplyEnvOverlay(t *testing.T) {
	t.Setenv("SYNCERD_GIT_GH_TOKEN", "from-env")
	g := validGit()
	g.Providers[0].Token = ""
	g.ApplyEnvOverlay()

	if g.Providers[0].Token != "from-env" {
		t.Errorf("Token = %q, want from-env", g.Providers[0].Token)
	}
}

func TestApplyEnvOverlayDoesNotOverrideExplicitValue(t *testing.T) {
	t.Setenv("SYNCERD_GIT_GH_TOKEN", "from-env")
	g := validGit()
	g.ApplyEnvOverlay()

	if g.Providers[0].Token != "ghp_token_value" {
		t.Errorf("explicit token must win, got %q", g.Providers[0].Token)
	}
}

func TestApplyEnvOverlayNormalisesName(t *testing.T) {
	t.Setenv("SYNCERD_GIT_GH_MIRRORS_TOKEN", "from-env")
	g := validGit()
	g.Providers[0].Name = "gh-mirrors"
	g.Providers[0].Token = ""
	g.Mirrors[0].Source = "gh-mirrors"
	g.ApplyEnvOverlay()

	if g.Providers[0].Token != "from-env" {
		t.Errorf("hyphen in provider name must map to underscore, got %q", g.Providers[0].Token)
	}
}

func TestSecretsCollectsAllTokens(t *testing.T) {
	g := validGit()
	secrets := g.Secrets()
	if len(secrets) != 2 {
		t.Fatalf("got %d secrets, want 2: %v", len(secrets), secrets)
	}
}

func TestValidateGitSyncRequiresToken(t *testing.T) {
	g := validGit()
	g.Providers[0].Token = ""
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("expected a token error, got %v", err)
	}
	if !strings.Contains(err.Error(), "SYNCERD_GIT_GH_TOKEN") {
		t.Errorf("error should name the environment variable, got %v", err)
	}
}

func TestValidateGitSyncAzureEntraNeedsNoToken(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "ado", Type: "azuredevops", Owner: "org", Project: "platform", Auth: "entra",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-ado", Source: "gh", Destination: "ado"})
	cfg := &Config{Git: g}
	if err := cfg.ValidateGitSync(); err != nil {
		t.Fatalf("entra mode must not require a token, got %v", err)
	}
}

func TestValidateGitSyncAzurePatNeedsToken(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "ado", Type: "azuredevops", Owner: "org", Project: "platform",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-ado", Source: "gh", Destination: "ado"})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "pat mode") {
		t.Fatalf("an unset auth means pat and must require a token, got %v", err)
	}
}

func TestValidateGitSyncCodeCommitNeedsNoToken(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "cc", Type: "codecommit", Region: "us-east-1",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-cc", Source: "gh", Destination: "cc"})
	cfg := &Config{Git: g}
	if err := cfg.ValidateGitSync(); err != nil {
		t.Fatalf("codecommit uses an IAM role and must not require a token, got %v", err)
	}
}

func TestValidateGitSyncCodeCommitRejectsHalfCredentialPair(t *testing.T) {
	g := validGit()
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "cc", Type: "codecommit", Region: "us-east-1", GitUsername: "only-user",
	})
	g.Mirrors = append(g.Mirrors, MirrorConfig{Name: "gh-to-cc", Source: "gh", Destination: "cc"})
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "set both or neither") {
		t.Fatalf("expected a half credential pair error, got %v", err)
	}
}

func TestValidateGitSyncRejectsCollidingEnvKeys(t *testing.T) {
	g := validGit()
	g.Providers[0].Name = "gh-mirrors"
	g.Providers = append(g.Providers, GitProviderConfig{
		Name: "gh.mirrors", Type: "github", Owner: "other", Token: "ghp_other_token",
	})
	g.Mirrors[0].Source = "gh-mirrors"
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "SYNCERD_GIT_GH_MIRRORS") {
		t.Fatalf("expected an environment prefix collision error, got %v", err)
	}
}

func TestValidateGitSyncRejectsBadVisibility(t *testing.T) {
	g := validGit()
	g.Mirrors[0].Visibility = "pubic"
	cfg := &Config{Git: g}
	err := cfg.ValidateGitSync()
	if err == nil || !strings.Contains(err.Error(), "visibility") {
		t.Fatalf("expected a visibility error, got %v", err)
	}
}
