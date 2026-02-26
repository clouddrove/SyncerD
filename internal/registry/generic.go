package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// GenericRegistry is a generic OCI registry implementation.
// It uses authn.DefaultKeychain (docker credential helpers / docker config)
// for authentication.
type GenericRegistry struct {
	registry string
	keychain authn.Keychain
}

func NewGenericRegistry(registry string) *GenericRegistry {
	return &GenericRegistry{
		registry: registry,
		keychain: authn.DefaultKeychain,
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

func (r *GenericRegistry) PullImage(ctx context.Context, image, tag string) (string, error) {
	ref := fmt.Sprintf("%s/%s:%s", r.registry, image, tag)
	d, err := crane.Digest(ref, crane.WithContext(ctx), crane.WithAuthFromKeychain(r.keychain))
	if err != nil {
		return "", err
	}
	return d, nil
}

func (r *GenericRegistry) PushImage(ctx context.Context, image, tag, digest string) error {
	_ = ctx
	_ = image
	_ = tag
	_ = digest
	return fmt.Errorf("push not supported: use crane.Copy/Push in sync engine")
}

func (r *GenericRegistry) GetRegistryURL() string {
	return r.registry
}
