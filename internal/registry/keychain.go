package registry

import (
	"github.com/google/go-containerregistry/pkg/authn"
)

// SourceAwareKeychain returns configured auth for Docker Hub, and falls back to
// the default keychain for everything else.
type SourceAwareKeychain struct {
	SourceAuth authn.Authenticator
	Fallback   authn.Keychain
}

func (k SourceAwareKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	if k.Fallback == nil {
		k.Fallback = authn.DefaultKeychain
	}

	reg := resource.RegistryStr()
	switch reg {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		if k.SourceAuth != nil && k.SourceAuth != authn.Anonymous {
			return k.SourceAuth, nil
		}
	}

	return k.Fallback.Resolve(resource)
}

