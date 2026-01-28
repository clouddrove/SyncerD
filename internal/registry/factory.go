package registry

import (
	"context"
	"fmt"
)

// RegistryFactory creates registry instances based on type
type RegistryFactory struct{}

func NewRegistryFactory() *RegistryFactory {
	return &RegistryFactory{}
}

func (f *RegistryFactory) CreateSourceRegistry(registryType, registry, username, password, token string) (Registry, error) {
	switch registryType {
	case "dockerhub", "docker.io":
		return NewDockerHubRegistry(registry, username, password, token), nil
	default:
		return nil, fmt.Errorf("unsupported source registry type: %s", registryType)
	}
}

func (f *RegistryFactory) CreateDestinationRegistry(registryType, registry string, region string, auth map[string]string) (Registry, error) {
	// Production-ready approach:
	// - treat ECR/ACR/GCR/GHCR as generic OCI registries
	// - rely on docker credential config (authn.DefaultKeychain) in the runtime environment
	//
	// The registryType is kept for config compatibility but does not change behavior here.
	_ = registryType
	_ = region
	_ = auth
	if registry == "" {
		return nil, fmt.Errorf("destination registry is required")
	}
	return NewGenericRegistry(registry), nil
}

func (f *RegistryFactory) TestConnection(ctx context.Context, reg Registry) error {
	return reg.Authenticate(ctx)
}
