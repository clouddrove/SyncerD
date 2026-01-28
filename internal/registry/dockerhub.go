package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type DockerHubRegistry struct {
	registry string
	username string
	password string
	token    string
	client   *http.Client
}

func NewDockerHubRegistry(registry, username, password, token string) *DockerHubRegistry {
	return &DockerHubRegistry{
		registry: registry,
		username: username,
		password: password,
		token:    token,
		client:   &http.Client{},
	}
}

func (r *DockerHubRegistry) Authenticate(ctx context.Context) error {
	// Docker Hub authentication is handled via go-containerregistry
	// Token-based auth is preferred if available
	return nil
}

func (r *DockerHubRegistry) ListTags(ctx context.Context, imageName string) ([]string, error) {
	// Docker Hub API endpoint for listing tags
	apiURL := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags", imageName)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	} else if r.username != "" && r.password != "" {
		req.SetBasicAuth(r.username, r.password)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list tags: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Count   int `json:"count"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
		Next string `json:"next"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	tags := make([]string, 0, len(result.Results))
	for _, tag := range result.Results {
		tags = append(tags, tag.Name)
	}

	// Handle pagination
	for result.Next != "" {
		req, err := http.NewRequestWithContext(ctx, "GET", result.Next, nil)
		if err != nil {
			break
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		} else if r.username != "" && r.password != "" {
			req.SetBasicAuth(r.username, r.password)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			break
		}

		var nextResult struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
			Next string `json:"next"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&nextResult); err != nil {
			resp.Body.Close()
			break
		}

		for _, tag := range nextResult.Results {
			tags = append(tags, tag.Name)
		}
		result.Next = nextResult.Next
		resp.Body.Close()
	}

	return tags, nil
}

func (r *DockerHubRegistry) ImageExists(ctx context.Context, image, tag string) (bool, error) {
	ref, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", r.registry, image, tag))
	if err != nil {
		return false, err
	}

	var auth authn.Authenticator
	if r.token != "" {
		auth = &authn.Bearer{Token: r.token}
	} else if r.username != "" && r.password != "" {
		auth = &authn.Basic{
			Username: r.username,
			Password: r.password,
		}
	}

	_, err = remote.Head(ref, remote.WithAuth(auth), remote.WithContext(ctx))
	if err != nil {
		return false, nil // Image doesn't exist or error accessing
	}
	return true, nil
}

func (r *DockerHubRegistry) PullImage(ctx context.Context, image, tag string) (string, error) {
	ref, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", r.registry, image, tag))
	if err != nil {
		return "", err
	}

	var auth authn.Authenticator
	if r.token != "" {
		auth = &authn.Bearer{Token: r.token}
	} else if r.username != "" && r.password != "" {
		auth = &authn.Basic{
			Username: r.username,
			Password: r.password,
		}
	}

	img, err := crane.Pull(ref.String(), crane.WithAuth(auth), crane.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("failed to pull image: %w", err)
	}

	digest, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("failed to get digest: %w", err)
	}

	return digest.String(), nil
}

func (r *DockerHubRegistry) PushImage(ctx context.Context, image, tag, digest string) error {
	// This is a source registry, push is not typically needed
	return fmt.Errorf("push not supported for source registry")
}

func (r *DockerHubRegistry) GetRegistryURL() string {
	if r.registry == "docker.io" {
		return "docker.io"
	}
	return r.registry
}

// NormalizeImageName normalizes Docker Hub image names
func NormalizeDockerHubImage(imageName string) string {
	// Remove docker.io prefix if present
	imageName = strings.TrimPrefix(imageName, "docker.io/")
	imageName = strings.TrimPrefix(imageName, "index.docker.io/")

	// If no namespace, assume library
	parts := strings.Split(imageName, "/")
	if len(parts) == 1 {
		return "library/" + parts[0]
	}

	return imageName
}
