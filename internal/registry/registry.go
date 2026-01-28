package registry

import (
	"context"
	"fmt"
)

// Registry interface for interacting with different container registries
type Registry interface {
	// Authenticate authenticates with the registry
	Authenticate(ctx context.Context) error

	// ListTags lists all tags for an image
	ListTags(ctx context.Context, image string) ([]string, error)

	// ImageExists checks if an image with the given tag exists
	ImageExists(ctx context.Context, image, tag string) (bool, error)

	// PullImage pulls an image from the registry
	PullImage(ctx context.Context, image, tag string) (string, error) // returns image digest

	// PushImage pushes an image to the registry
	PushImage(ctx context.Context, image, tag, digest string) error

	// GetRegistryURL returns the full registry URL
	GetRegistryURL() string
}

// ImageRef represents a full image reference
type ImageRef struct {
	Registry string
	Image    string
	Tag      string
}

func (r ImageRef) String() string {
	return fmt.Sprintf("%s/%s:%s", r.Registry, r.Image, r.Tag)
}
