package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// apiPullRequest is the subset of the GitHub pull request object SyncerD
// reads.
type apiPullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ClosedAt       *time.Time `json:"closed_at"`
	MergedAt       *time.Time `json:"merged_at"`
}

// toPullRequest converts the API representation to vcs.PullRequest.
//
// HeadRepoCloneURL is set only when the head lives outside the base
// repository. A head in the base repository already reaches the
// destination as an ordinary branch, so marking it a fork would mirror the
// same commits twice under two names.
//
// A head repository that has been deleted, the fork removed while its pull
// request stayed open, decodes as a null repo. Those commits are reachable
// from nowhere, and reporting an empty clone URL would misfile the pull
// request as a same repository head, so the head SHA is cleared and the
// caller drops it.
func (a apiPullRequest) toPullRequest() vcs.PullRequest {
	pr := vcs.PullRequest{
		Number:     a.Number,
		Title:      a.Title,
		Body:       a.Body,
		State:      pullRequestState(a),
		Draft:      a.Draft,
		Author:     vcs.Actor{Handle: a.User.Login},
		HeadBranch: a.Head.Ref,
		HeadSHA:    a.Head.SHA,
		BaseBranch: a.Base.Ref,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
		ClosedAt:   a.ClosedAt,
		MergedAt:   a.MergedAt,
		MergeSHA:   a.MergeCommitSHA,
		WebURL:     a.HTMLURL,
	}
	for _, l := range a.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}

	if a.Head.Repo == nil {
		pr.HeadSHA = ""
		return pr
	}
	base := ""
	if a.Base.Repo != nil {
		base = a.Base.Repo.FullName
	}
	if a.Head.Repo.FullName != base {
		pr.HeadRepoCloneURL = a.Head.Repo.CloneURL
	}
	return pr
}

// pullRequestState maps GitHub's two state values plus merged_at onto the
// three state model. GitHub reports a merged pull request as closed.
func pullRequestState(a apiPullRequest) vcs.PRState {
	switch {
	case a.MergedAt != nil:
		return vcs.PRMerged
	case a.State == "closed":
		return vcs.PRClosed
	default:
		return vcs.PROpen
	}
}

// ListPullRequests returns the pull requests of one repository, following
// pagination. repoPath is the full owner/name path reported by ListRepos.
// Open pull requests are the only ones listed today; opts.States is
// accepted so a later phase can widen it without changing the interface,
// and anything else is rejected rather than silently ignored.
func (p *Provider) ListPullRequests(ctx context.Context, repoPath string, opts vcs.PRListOptions) ([]vcs.PullRequest, error) {
	for _, s := range opts.States {
		if s != vcs.PROpen {
			return nil, fmt.Errorf("github: listing %s pull requests is not supported", s)
		}
	}

	pageURL := fmt.Sprintf("%s/repos/%s/pulls?per_page=100&state=open&sort=updated&direction=desc", p.apiURL, repoPath)

	var out []vcs.PullRequest
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("github: pull request pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("github: pull request pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, header, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}

		var page []apiPullRequest
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("github: decode pull request list: %w", err)
		}
		for _, a := range page {
			out = append(out, a.toPullRequest())
		}

		next, nerr := resolveNext(pageURL, header.Get("Link"))
		if nerr != nil {
			return out, nerr
		}
		pageURL = next
	}

	return out, nil
}
