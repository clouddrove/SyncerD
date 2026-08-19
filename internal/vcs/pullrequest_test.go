package vcs

import "testing"

func TestValidateBranchPrefix(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"default", "syncerd/pr", false},
		{"single segment", "mirrored", false},
		{"empty", "", true},
		{"absolute", "/syncerd/pr", true},
		{"trailing slash", "syncerd/pr/", true},
		{"double slash", "syncerd//pr", true},
		{"refs prefixed", "refs/heads/syncerd", true},
		{"space", "syncerd pr", true},
		{"dot dot", "syncerd/../pr", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBranchPrefix(tc.prefix)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateBranchPrefix(%q) = nil, want an error", tc.prefix)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateBranchPrefix(%q) = %v, want nil", tc.prefix, err)
			}
		})
	}
}

func TestPRBranch(t *testing.T) {
	if got := PRBranch("syncerd/pr", 42); got != "syncerd/pr/42" {
		t.Errorf("PRBranch = %q, want syncerd/pr/42", got)
	}
}

func TestIsFork(t *testing.T) {
	if (PullRequest{}).IsFork() {
		t.Error("a pull request with no head repository clone URL is not a fork")
	}
	if !(PullRequest{HeadRepoCloneURL: "https://github.com/other/repo.git"}).IsFork() {
		t.Error("a pull request carrying a head repository clone URL is a fork")
	}
}
