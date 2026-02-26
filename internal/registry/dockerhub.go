package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
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
	if r.username == "" && r.token == "" {
		return nil // anonymous
	}
	if r.username != "" && r.password != "" {
		body, _ := json.Marshal(map[string]string{"username": r.username, "password": r.password})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://hub.docker.com/v2/users/login/", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.client.Do(req)
		if err != nil {
			return fmt.Errorf("reach Docker Hub: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("docker hub authentication failed: invalid credentials")
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("docker hub authentication failed: %s", resp.Status)
		}
		return nil
	}
	// Token: verify it's accepted by the API
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://hub.docker.com/v2/repositories/library/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("reach Docker Hub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("docker hub token authentication failed")
	}
	return nil
}

func (r *DockerHubRegistry) ListTags(ctx context.Context, imageName string) ([]string, error) {
	apiURL := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags?page_size=100", imageName)

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

	for result.Next != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", result.Next, nil)
		if err != nil {
			return nil, fmt.Errorf("build pagination request: %w", err)
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		} else if r.username != "" && r.password != "" {
			req.SetBasicAuth(r.username, r.password)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch next page: %w", err)
		}

		var nextResult struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
			Next string `json:"next"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&nextResult); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode next page: %w", err)
		}
		resp.Body.Close()

		for _, tag := range nextResult.Results {
			tags = append(tags, tag.Name)
		}
		result.Next = nextResult.Next
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
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
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
	return fmt.Errorf("push not supported for source registry")
}

func (r *DockerHubRegistry) GetRegistryURL() string {
	if r.registry == "docker.io" {
		return "docker.io"
	}
	return r.registry
}

// NormalizeDockerHubImage normalizes Docker Hub image names
func NormalizeDockerHubImage(imageName string) string {
	imageName = strings.TrimPrefix(imageName, "docker.io/")
	imageName = strings.TrimPrefix(imageName, "index.docker.io/")

	parts := strings.Split(imageName, "/")
	if len(parts) == 1 {
		return "library/" + parts[0]
	}

	return imageName
}
