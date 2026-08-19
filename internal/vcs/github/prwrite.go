package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// FindPullRequest locates a pull request by head branch. The head branch of
// a mirrored pull request is unique per source pull request, so this is what
// lets a run recover from a lost state file without creating a duplicate.
//
// The head filter is qualified with the owner, which is what GitHub expects
// and what keeps a same named branch in a fork from matching.
func (p *Provider) FindPullRequest(ctx context.Context, repoPath, headBranch string) (vcs.PullRequest, bool, error) {
	owner := repoPath
	if i := strings.Index(repoPath, "/"); i > 0 {
		owner = repoPath[:i]
	}

	endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=all&per_page=100&head=%s",
		p.apiURL, repoPath, url.QueryEscape(owner+":"+headBranch))

	body, _, err := p.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return vcs.PullRequest{}, false, err
	}

	var page []apiPullRequest
	if err := json.Unmarshal(body, &page); err != nil {
		return vcs.PullRequest{}, false, fmt.Errorf("github: decode pull request lookup: %w", err)
	}
	if len(page) == 0 {
		return vcs.PullRequest{}, false, nil
	}

	// GitHub returns newest first. A head branch reused across a closed and
	// a reopened pull request yields more than one; the newest is the one
	// this mirror is maintaining.
	return page[0].toPullRequest(), true, nil
}

// CreatePullRequest opens a pull request at the destination and applies its
// labels. Label failures do not fail the creation: a pull request without
// its labels is a working mirror, and a creation rolled back over a label
// would leave the conversation with nowhere to go.
func (p *Provider) CreatePullRequest(ctx context.Context, repoPath string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	payload := map[string]any{
		"title": spec.Title,
		"body":  spec.Body,
		"head":  spec.HeadBranch,
		"base":  spec.BaseBranch,
		"draft": spec.Draft,
	}

	body, _, err := p.do(ctx, http.MethodPost, fmt.Sprintf("%s/repos/%s/pulls", p.apiURL, repoPath), payload)
	if err != nil {
		return vcs.PullRequest{}, err
	}

	var created apiPullRequest
	if err := json.Unmarshal(body, &created); err != nil {
		return vcs.PullRequest{}, fmt.Errorf("github: decode created pull request: %w", err)
	}

	pr := created.toPullRequest()
	if len(spec.Labels) > 0 {
		if err := p.setLabels(ctx, repoPath, pr.Number, spec.Labels); err != nil {
			return pr, fmt.Errorf("github: pull request %d created but labels failed: %w", pr.Number, err)
		}
	}
	return pr, nil
}

// UpdatePullRequest rewrites the mutable fields of an existing pull request.
//
// The head and base branches are not updated: GitHub allows changing the
// base but not the head, and a source pull request that retargets its base
// is rare enough that recreating on the next divergence is clearer than a
// partial update. The base is sent anyway so a retarget is picked up where
// GitHub accepts it.
func (p *Provider) UpdatePullRequest(ctx context.Context, repoPath string, number int, spec vcs.PullRequestSpec) error {
	payload := map[string]any{
		"title": spec.Title,
		"body":  spec.Body,
		"base":  spec.BaseBranch,
	}

	if _, _, err := p.do(ctx, http.MethodPatch, fmt.Sprintf("%s/repos/%s/pulls/%d", p.apiURL, repoPath, number), payload); err != nil {
		return err
	}

	if len(spec.Labels) > 0 {
		if err := p.setLabels(ctx, repoPath, number, spec.Labels); err != nil {
			return fmt.Errorf("github: pull request %d updated but labels failed: %w", number, err)
		}
	}
	return nil
}

// ClosePullRequest closes a pull request.
//
// There is no merge here, deliberately. Merging at the destination would
// create a merge commit that differs from the source's, which the ref
// mirror then force overwrites on the base branch, so the destination would
// report a merge whose commit no longer exists. A merged source pull
// request is closed and commented instead.
func (p *Provider) ClosePullRequest(ctx context.Context, repoPath string, number int) error {
	return p.setState(ctx, repoPath, number, "closed")
}

// ReopenPullRequest moves a closed pull request back to open. GitHub
// supports this; Bitbucket and CodeCommit do not, which is why it lives on
// its own interface.
func (p *Provider) ReopenPullRequest(ctx context.Context, repoPath string, number int) error {
	return p.setState(ctx, repoPath, number, "open")
}

func (p *Provider) setState(ctx context.Context, repoPath string, number int, state string) error {
	payload := map[string]any{"state": state}
	_, _, err := p.do(ctx, http.MethodPatch, fmt.Sprintf("%s/repos/%s/pulls/%d", p.apiURL, repoPath, number), payload)
	return err
}

// setLabels replaces the labels on a pull request. GitHub creates a label
// that does not exist yet as a side effect of this call, so no separate
// creation step is needed.
func (p *Provider) setLabels(ctx context.Context, repoPath string, number int, labels []string) error {
	payload := map[string]any{"labels": labels}
	_, _, err := p.do(ctx, http.MethodPut,
		fmt.Sprintf("%s/repos/%s/issues/%d/labels", p.apiURL, repoPath, number), payload)
	return err
}
