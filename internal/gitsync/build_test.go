package gitsync

import (
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
			{Name: "gh-to-gl", Source: "gh", Destination: "gl"},
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
	if m.Source == nil || m.Dest == nil || m.DestEnsurer == nil {
		t.Errorf("mirror is missing a role: %+v", m)
	}
	if m.PushMode != PushMirror {
		t.Errorf("PushMode = %q", m.PushMode)
	}
	if !m.Filter.SkipArchived || !m.Filter.SkipForks {
		t.Errorf("filter defaults not applied: %+v", m.Filter)
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
