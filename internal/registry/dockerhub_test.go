package registry

import "testing"

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
