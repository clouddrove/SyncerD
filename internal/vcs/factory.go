package vcs

import "fmt"

// ProviderConfig is the provider independent construction input. It mirrors
// config.GitProviderConfig without importing internal/config, so the vcs
// package stays free of configuration concerns.
type ProviderConfig struct {
	Name        string
	Type        string
	Owner       string
	APIURL      string
	Token       string
	Email       string
	Project     string
	Auth        string
	Region      string
	GitUsername string
	GitPassword string
}

// Constructor builds a provider from its configuration.
type Constructor func(ProviderConfig) (Provider, error)

// Registry maps provider type strings to constructors. The provider
// subpackages import vcs, so vcs must not import them; callers register
// constructors instead.
type Registry struct {
	constructors map[string]Constructor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{constructors: make(map[string]Constructor)}
}

// Register adds a constructor for a provider type.
func (r *Registry) Register(typeName string, c Constructor) {
	r.constructors[typeName] = c
}

// Build constructs the provider described by cfg.
func (r *Registry) Build(cfg ProviderConfig) (Provider, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	c, ok := r.constructors[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("provider %q has unsupported type %q", cfg.Name, cfg.Type)
	}
	return c(cfg)
}
