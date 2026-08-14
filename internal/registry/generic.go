package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// GenericRegistry is a generic OCI registry implementation.
// It uses docker credential helpers / docker config for authentication, plus
// native AWS authentication for private ECR hosts (see ECRKeychain).
type GenericRegistry struct {
	registry string
	keychain authn.Keychain
}

func NewGenericRegistry(registry string) *GenericRegistry {
	return NewGenericRegistryWithKeychain(registry, DefaultDestinationKeychain())
}

// NewGenericRegistryWithKeychain builds a registry that authenticates with the
// given keychain. A nil keychain means the destination default.
func NewGenericRegistryWithKeychain(registry string, keychain authn.Keychain) *GenericRegistry {
	if keychain == nil {
		keychain = DefaultDestinationKeychain()
	}
	return &GenericRegistry{
		registry: registry,
		keychain: keychain,
	}
}

func (r *GenericRegistry) Authenticate(ctx context.Context) error {
	// Generic registries don't support a single universal "ping" for auth that works
	// across providers without knowing a repository. We'll validate auth lazily
	// when we attempt Head/List/Copy for real images.
	_ = ctx
	return nil
}

func (r *GenericRegistry) ListTags(ctx context.Context, image string) ([]string, error) {
	repo, err := name.NewRepository(fmt.Sprintf("%s/%s", r.registry, image))
	if err != nil {
		return nil, err
	}
	return remote.List(repo, remote.WithContext(ctx), remote.WithAuthFromKeychain(r.keychain))
}

func (r *GenericRegistry) ImageExists(ctx context.Context, image, tag string) (bool, error) {
	ref, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", r.registry, image, tag))
	if err != nil {
		return false, err
	}
	_, err = remote.Head(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(r.keychain))
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *GenericRegistry) GetRegistryURL() string {
	return r.registry
}
