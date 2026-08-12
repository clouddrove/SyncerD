package vcs

import "testing"

func TestSanitizeCloneURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "azure devops shaped url with userinfo",
			raw:  "https://myorg@dev.azure.com/myorg/myproject/_git/repo",
			want: "https://dev.azure.com/myorg/myproject/_git/repo",
		},
		{
			name: "bitbucket shaped url with userinfo",
			raw:  "https://acct@bitbucket.org/myworkspace/repo.git",
			want: "https://bitbucket.org/myworkspace/repo.git",
		},
		{
			name: "url with user and password",
			raw:  "https://user:pass@example.com/a/b.git",
			want: "https://example.com/a/b.git",
		},
		{
			name: "clean url passes through byte identical",
			raw:  "https://github.com/clouddrove/terraform-aws-vpc.git",
			want: "https://github.com/clouddrove/terraform-aws-vpc.git",
		},
		{
			name: "unparseable string passes through unchanged",
			raw:  "://not a url",
			want: "://not a url",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeCloneURL(tc.raw); got != tc.want {
				t.Errorf("SanitizeCloneURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
