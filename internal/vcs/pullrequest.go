package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PRState is the provider independent lifecycle state of a pull request.
type PRState string

const (
	PROpen   PRState = "open"
	PRClosed PRState = "closed"
	PRMerged PRState = "merged"
)

// Actor is a source side identity. Handle is the provider login; the rest
// may be empty, since not every provider reports them.
type Actor struct {
	Handle      string
	DisplayName string
	Email       string
}

// PullRequest is one pull request, merge request, or Azure DevOps pull
// request, as reported by a source provider.
type PullRequest struct {
	Number     int
	Title      string
	Body       string
	State      PRState
	Draft      bool
	Author     Actor
	HeadBranch string
	HeadSHA    string
	BaseBranch string
	Labels     []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ClosedAt   *time.Time
	MergedAt   *time.Time
	MergeSHA   string

	// HeadRepoCloneURL is empty when the head branch lives in the source
	// repository itself, and set when it lives in a fork. It is what the
	// fetch path uses, since a fork's commits are reachable from the fork
	// and from nowhere else the mirror already visits.
	HeadRepoCloneURL string
	// WebURL is the pull request's page at the source. A later phase uses
	// it in the attribution header of the mirrored pull request.
	WebURL string
}

// IsFork reports whether the head branch lives outside the source
// repository. Only a fork head needs a synthetic branch: a head in the
// source repository already reaches the destination under its own name
// with the ordinary branch mirror.
func (p PullRequest) IsFork() bool { return p.HeadRepoCloneURL != "" }

// Comment is a top level discussion comment. It is declared alongside the
// rest of the model because the interfaces carrying it are what every
// provider implements; nothing reads or writes it yet.
type Comment struct {
	SourceID  string
	Author    Actor
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReviewComment is a comment anchored to a line of the diff.
type ReviewComment struct {
	Comment
	Path      string
	Line      int
	Side      string // LEFT or RIGHT
	CommitSHA string
	InReplyTo string
}

// Review is a review verdict on a pull request.
type Review struct {
	SourceID    string
	Author      Actor
	Body        string
	State       string // approved, changes_requested, commented
	SubmittedAt time.Time
}

// PRListOptions bounds a pull request listing.
type PRListOptions struct {
	// States selects which states to return. Empty means open only.
	States []PRState
	// Since bounds the listing to pull requests updated at or after this
	// time. The zero value means no lower bound.
	Since time.Time
}

// PullRequestLister discovers pull requests for one repository.
// Implemented by source providers that support pull request sync.
type PullRequestLister interface {
	ListPullRequests(ctx context.Context, repoPath string, opts PRListOptions) ([]PullRequest, error)
}

// ValidateBranchPrefix rejects a prefix that cannot safely name a branch
// namespace. The prefix is joined with a pull request number to form a
// branch under refs/heads, so it must be a relative, slash separated
// sequence of ordinary path components.
func ValidateBranchPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("branch_prefix is empty; it names the branch namespace pull request heads are pushed into")
	}
	if strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("branch_prefix %q must not begin or end with a slash", prefix)
	}
	if strings.HasPrefix(prefix, "refs/") {
		return fmt.Errorf("branch_prefix %q must not start with refs/; it is joined onto refs/heads/ already", prefix)
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "" {
			return fmt.Errorf("branch_prefix %q contains an empty path component", prefix)
		}
		if part == "." || part == ".." {
			return fmt.Errorf("branch_prefix %q contains a relative path component", prefix)
		}
		if strings.ContainsAny(part, " \t~^:?*[\\") {
			return fmt.Errorf("branch_prefix %q contains a character git refuses in a ref name", prefix)
		}
	}
	return nil
}

// PRBranch returns the branch name a pull request head is mirrored under,
// relative to refs/heads.
func PRBranch(prefix string, number int) string {
	return fmt.Sprintf("%s/%d", prefix, number)
}
