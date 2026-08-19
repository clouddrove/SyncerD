package registry

import (
	"net/http"
	"testing"
)

func TestNormalizeDockerHubImage(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"nginx", "library/nginx"},
		{"library/nginx", "library/nginx"},
		{"clouddrove/syncerd", "clouddrove/syncerd"},
		{"docker.io/nginx", "library/nginx"},
		{"docker.io/clouddrove/syncerd", "clouddrove/syncerd"},
		{"index.docker.io/nginx", "library/nginx"},
		{"index.docker.io/clouddrove/syncerd", "clouddrove/syncerd"},
	}
	for _, c := range cases {
		if got := NormalizeDockerHubImage(c.in); got != c.want {
			t.Errorf("NormalizeDockerHubImage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDockerHubUsesTheSessionTokenFromLogin(t *testing.T) {
	// The Hub API authenticates with the JWT a login returns, never with
	// basic auth. Discarding it made every authenticated listing fall back
	// to a scheme the API rejects, so a private repository answered 404 and
	// the failure was blamed on the image.
	r := &DockerHubRegistry{username: "u", password: "p", client: &http.Client{}}
	r.setJWT("session-jwt")

	req, err := http.NewRequest(http.MethodGet, "https://hub.docker.com/v2/repositories/acme/private/tags", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	r.authorize(req)

	if got := req.Header.Get("Authorization"); got != "Bearer session-jwt" {
		t.Errorf("Authorization = %q, want the session token", got)
	}
	if _, _, ok := req.BasicAuth(); ok {
		t.Error("basic auth must not be sent once a session token exists")
	}
}

func TestDockerHubPrefersAConfiguredTokenOverBasic(t *testing.T) {
	r := &DockerHubRegistry{username: "u", password: "p", token: "pat", client: &http.Client{}}

	req, _ := http.NewRequest(http.MethodGet, "https://hub.docker.com/v2/repositories/acme/x/tags", nil)
	r.authorize(req)

	if got := req.Header.Get("Authorization"); got != "Bearer pat" {
		t.Errorf("Authorization = %q, want the configured token", got)
	}
}

func TestDockerHubSendsNothingWhenAnonymous(t *testing.T) {
	r := &DockerHubRegistry{client: &http.Client{}}

	req, _ := http.NewRequest(http.MethodGet, "https://hub.docker.com/v2/repositories/library/busybox/tags", nil)
	r.authorize(req)

	if req.Header.Get("Authorization") != "" {
		t.Error("an anonymous registry must send no credential")
	}
}
