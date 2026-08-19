package vcs

import (
	"context"
	"testing"
)

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

// fakeWriter is a compile time check that the destination side interfaces
// are implementable, and a place to prove the never merge rule has no
// method to call in the first place.
type fakeWriter struct{}

func (fakeWriter) FindPullRequest(context.Context, string, string) (PullRequest, bool, error) {
	return PullRequest{}, false, nil
}
func (fakeWriter) CreatePullRequest(context.Context, string, PullRequestSpec) (PullRequest, error) {
	return PullRequest{}, nil
}
func (fakeWriter) UpdatePullRequest(context.Context, string, int, PullRequestSpec) error { return nil }
func (fakeWriter) ClosePullRequest(context.Context, string, int) error                   { return nil }

func TestPullRequestWriterIsImplementable(t *testing.T) {
	var w PullRequestWriter = fakeWriter{}
	if _, ok, err := w.FindPullRequest(context.Background(), "acme/widget", "syncerd/pr/7"); ok || err != nil {
		t.Fatalf("FindPullRequest = %v, %v", ok, err)
	}
}

func TestWriterInterfaceHasNoMergeMethod(t *testing.T) {
	// A merge at the destination would diverge from the mirrored base
	// branch, so the capability must not exist to be reached for by
	// accident. This asserts the shape of the interface itself.
	if _, ok := any(fakeWriter{}).(interface {
		MergePullRequest(context.Context, string, int) error
	}); ok {
		t.Fatal("the destination writer must not expose a merge")
	}
}
