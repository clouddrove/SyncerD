package vcs

import (
	"context"
	"strings"
	"testing"
)

type stubProvider struct{ name string }

func (s *stubProvider) Name() string                                     { return s.name }
func (s *stubProvider) Type() string                                     { return "stub" }
func (s *stubProvider) CloneURL(path string) string                      { return "https://stub/" + path }
func (s *stubProvider) SupportsNesting() bool                            { return true }
func (s *stubProvider) GitCredential(context.Context) (GitCredential, error) {
	return GitCredential{}, nil
}

func TestRegistryBuild(t *testing.T) {
	r := NewRegistry()
	r.Register("stub", func(cfg ProviderConfig) (Provider, error) {
		return &stubProvider{name: cfg.Name}, nil
	})

	p, err := r.Build(ProviderConfig{Name: "one", Type: "stub"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p.Name() != "one" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestRegistryUnknownType(t *testing.T) {
	r := NewRegistry()
	_, err := r.Build(ProviderConfig{Name: "x", Type: "nope"})
	if err == nil {
		t.Fatal("expected an error for an unregistered type")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name the type, got %v", err)
	}
}

func TestRegistryRequiresName(t *testing.T) {
	r := NewRegistry()
	r.Register("stub", func(cfg ProviderConfig) (Provider, error) {
		return &stubProvider{name: cfg.Name}, nil
	})
	if _, err := r.Build(ProviderConfig{Type: "stub"}); err == nil {
		t.Fatal("expected an error when name is empty")
	}
}
