package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// apiPullRequest is the subset of the Bitbucket pull request object SyncerD
// reads.
type apiPullRequest struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Draft       bool   `json:"draft"`
	Author      struct {
		Nickname    string `json:"nickname"`
		DisplayName string `json:"display_name"`
	} `json:"author"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Repository struct {
			FullName string `json:"full_name"`
			Links    struct {
				Clone []struct {
					Name string `json:"name"`
					HRef string `json:"href"`
				} `json:"clone"`
			} `json:"links"`
		} `json:"repository"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	} `json:"destination"`
	MergeCommit *struct {
		Hash string `json:"hash"`
	} `json:"merge_commit"`
	Links struct {
		HTML struct {
			HRef string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	CreatedOn time.Time `json:"created_on"`
	UpdatedOn time.Time `json:"updated_on"`
}

func (a apiPullRequest) toPullRequest() vcs.PullRequest {
	pr := vcs.PullRequest{
		Number:     a.ID,
		Title:      a.Title,
		Body:       a.Description,
		State:      pullRequestState(a.State),
		Draft:      a.Draft,
		Author:     vcs.Actor{Handle: a.Author.Nickname, DisplayName: a.Author.DisplayName},
		HeadBranch: a.Source.Branch.Name,
		HeadSHA:    a.Source.Commit.Hash,
		BaseBranch: a.Destination.Branch.Name,
		CreatedAt:  a.CreatedOn,
		UpdatedAt:  a.UpdatedOn,
		WebURL:     a.Links.HTML.HRef,
	}
	if a.MergeCommit != nil {
		pr.MergeSHA = a.MergeCommit.Hash
	}
	// A pull request whose source repository differs from its destination
	// is Bitbucket's fork case.
	if a.Source.Repository.FullName != "" && a.Source.Repository.FullName != a.Destination.Repository.FullName {
		for _, l := range a.Source.Repository.Links.Clone {
			if l.Name == "https" {
				pr.HeadRepoCloneURL = vcs.SanitizeCloneURL(l.HRef)
				break
			}
		}
	}
	return pr
}

// pullRequestState maps Bitbucket's four states onto the three state model.
// SUPERSEDED means a newer pull request replaced this one, which for a
// mirror reads the same as declined: it is over and it did not merge.
func pullRequestState(state string) vcs.PRState {
	switch strings.ToUpper(state) {
	case "MERGED":
		return vcs.PRMerged
	case "DECLINED", "SUPERSEDED":
		return vcs.PRClosed
	default:
		return vcs.PROpen
	}
}

// prPage is one page of the pull request listing.
type prPage struct {
	Values []apiPullRequest `json:"values"`
	Next   string           `json:"next"`
}

// ListPullRequests returns the open pull requests of one repository.
func (p *Provider) ListPullRequests(ctx context.Context, repoPath string, opts vcs.PRListOptions) ([]vcs.PullRequest, error) {
	for _, s := range opts.States {
		if s != vcs.PROpen {
			return nil, fmt.Errorf("bitbucket: listing %s pull requests is not supported", s)
		}
	}
	return p.listPullRequests(ctx, fmt.Sprintf("%s/repositories/%s/pullrequests?state=OPEN&pagelen=50", p.apiURL, repoPath))
}

// FindPullRequest locates a pull request by its source branch.
//
// Bitbucket has no source branch parameter, so this goes through the
// generic query language instead.
func (p *Provider) FindPullRequest(ctx context.Context, repoPath, headBranch string) (vcs.PullRequest, bool, error) {
	q := url.QueryEscape(fmt.Sprintf(`source.branch.name="%s"`, headBranch))
	endpoint := fmt.Sprintf("%s/repositories/%s/pullrequests?state=OPEN&state=MERGED&state=DECLINED&pagelen=50&q=%s",
		p.apiURL, repoPath, q)

	found, err := p.listPullRequests(ctx, endpoint)
	if err != nil {
		return vcs.PullRequest{}, false, err
	}
	if len(found) == 0 {
		return vcs.PullRequest{}, false, nil
	}
	return found[0], true, nil
}

// listPullRequests walks the "next" chain of a pull request listing.
func (p *Provider) listPullRequests(ctx context.Context, pageURL string) ([]vcs.PullRequest, error) {
	var out []vcs.PullRequest
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("bitbucket: pull request pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("bitbucket: pull request pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, _, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}
		var page prPage
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("bitbucket: decode pull request list: %w", err)
		}
		for _, a := range page.Values {
			out = append(out, a.toPullRequest())
		}
		pageURL = page.Next
	}
	return out, nil
}

// CreatePullRequest opens a pull request.
//
// Labels are dropped: Bitbucket has no label or tag concept on a pull
// request at all. The caller logs that once per run rather than per pull
// request.
func (p *Provider) CreatePullRequest(ctx context.Context, repoPath string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	payload := map[string]any{
		"title":       spec.Title,
		"description": spec.Body,
		"draft":       spec.Draft,
		"source":      map[string]any{"branch": map[string]any{"name": spec.HeadBranch}},
		"destination": map[string]any{"branch": map[string]any{"name": spec.BaseBranch}},
	}

	body, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repositories/%s/pullrequests", p.apiURL, repoPath), payload)
	if err != nil {
		return vcs.PullRequest{}, err
	}

	var created apiPullRequest
	if err := json.Unmarshal(body, &created); err != nil {
		return vcs.PullRequest{}, fmt.Errorf("bitbucket: decode created pull request: %w", err)
	}
	return created.toPullRequest(), nil
}

// UpdatePullRequest rewrites the mutable fields of a pull request.
func (p *Provider) UpdatePullRequest(ctx context.Context, repoPath string, number int, spec vcs.PullRequestSpec) error {
	payload := map[string]any{
		"title":       spec.Title,
		"description": spec.Body,
		"destination": map[string]any{"branch": map[string]any{"name": spec.BaseBranch}},
	}
	_, _, err := p.do(ctx, http.MethodPut,
		fmt.Sprintf("%s/repositories/%s/pullrequests/%d", p.apiURL, repoPath, number), payload)
	return err
}

// ClosePullRequest declines a pull request, which is Bitbucket's close.
//
// Bitbucket has no reopen endpoint, so this provider deliberately does not
// implement vcs.PullRequestReopener. A destination pull request declined by
// hand while its source is still open stays declined, and SyncerD says so
// rather than opening a second pull request for the same work.
func (p *Provider) ClosePullRequest(ctx context.Context, repoPath string, number int) error {
	_, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repositories/%s/pullrequests/%d/decline", p.apiURL, repoPath, number), nil)
	return err
}

// apiComment is a Bitbucket pull request comment.
type apiComment struct {
	ID      int64 `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User struct {
		Nickname    string `json:"nickname"`
		DisplayName string `json:"display_name"`
	} `json:"user"`
	CreatedOn time.Time `json:"created_on"`
	UpdatedOn time.Time `json:"updated_on"`
	Deleted   bool      `json:"deleted"`
	Inline    *struct {
		Path string `json:"path"`
		From *int   `json:"from"`
		To   *int   `json:"to"`
	} `json:"inline"`
	Parent *struct {
		ID int64 `json:"id"`
	} `json:"parent"`
}

func (a apiComment) toComment() vcs.Comment {
	return vcs.Comment{
		SourceID:  strconv.FormatInt(a.ID, 10),
		Author:    vcs.Actor{Handle: a.User.Nickname, DisplayName: a.User.DisplayName},
		Body:      a.Content.Raw,
		CreatedAt: a.CreatedOn,
		UpdatedAt: a.UpdatedOn,
	}
}

// commentPage is one page of the comment listing.
type commentPage struct {
	Values []apiComment `json:"values"`
	Next   string       `json:"next"`
}

// ListComments returns the discussion comments on a pull request.
func (p *Provider) ListComments(ctx context.Context, repoPath string, number int) ([]vcs.Comment, error) {
	raw, err := p.listComments(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}
	out := make([]vcs.Comment, 0, len(raw))
	for _, a := range raw {
		if a.Inline != nil || a.Deleted {
			continue
		}
		out = append(out, a.toComment())
	}
	return out, nil
}

// ListReviewComments returns the comments anchored to a line of the diff.
//
// Bitbucket reports no commit SHA on an inline comment, so CommitSHA and
// BaseSHA stay empty. A destination that needs them, GitLab or CodeCommit,
// downgrades the comment to a discussion comment through the path the
// engine already has.
func (p *Provider) ListReviewComments(ctx context.Context, repoPath string, number int) ([]vcs.ReviewComment, error) {
	raw, err := p.listComments(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}
	out := make([]vcs.ReviewComment, 0)
	for _, a := range raw {
		if a.Inline == nil || a.Deleted {
			continue
		}
		rc := vcs.ReviewComment{Comment: a.toComment(), Path: a.Inline.Path, Side: "RIGHT"}
		switch {
		case a.Inline.To != nil:
			rc.Line = *a.Inline.To
		case a.Inline.From != nil:
			rc.Line = *a.Inline.From
			rc.Side = "LEFT"
		}
		if a.Parent != nil {
			rc.InReplyTo = strconv.FormatInt(a.Parent.ID, 10)
		}
		out = append(out, rc)
	}
	return out, nil
}

// ListReviews returns the approve and request changes verdicts, read from
// the participants on the pull request.
func (p *Provider) ListReviews(ctx context.Context, repoPath string, number int) ([]vcs.Review, error) {
	body, _, err := p.do(ctx, http.MethodGet,
		fmt.Sprintf("%s/repositories/%s/pullrequests/%d", p.apiURL, repoPath, number), nil)
	if err != nil {
		return nil, err
	}

	var full struct {
		Participants []struct {
			User struct {
				Nickname    string `json:"nickname"`
				DisplayName string `json:"display_name"`
				UUID        string `json:"uuid"`
			} `json:"user"`
			State          string     `json:"state"`
			ParticipatedOn *time.Time `json:"participated_on"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(body, &full); err != nil {
		return nil, fmt.Errorf("bitbucket: decode participants: %w", err)
	}

	out := make([]vcs.Review, 0, len(full.Participants))
	for _, part := range full.Participants {
		if part.State == "" {
			// Neither approved nor requested changes, so there is no
			// verdict to mirror.
			continue
		}
		r := vcs.Review{
			SourceID: part.User.UUID,
			Author:   vcs.Actor{Handle: part.User.Nickname, DisplayName: part.User.DisplayName},
			State:    strings.ToLower(part.State),
		}
		if part.ParticipatedOn != nil {
			r.SubmittedAt = *part.ParticipatedOn
		}
		out = append(out, r)
	}
	return out, nil
}

// listComments walks the paginated comment listing for one pull request.
func (p *Provider) listComments(ctx context.Context, repoPath string, number int) ([]apiComment, error) {
	pageURL := fmt.Sprintf("%s/repositories/%s/pullrequests/%d/comments?pagelen=100", p.apiURL, repoPath, number)

	var out []apiComment
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("bitbucket: comment pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("bitbucket: comment pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, _, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}
		var page commentPage
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("bitbucket: decode comment list: %w", err)
		}
		out = append(out, page.Values...)
		pageURL = page.Next
	}
	return out, nil
}

// CreateComment posts a discussion comment.
func (p *Provider) CreateComment(ctx context.Context, repoPath string, number int, body string) (string, error) {
	return p.postComment(ctx, repoPath, number, map[string]any{
		"content": map[string]any{"raw": body},
	})
}

// UpdateComment rewrites a comment SyncerD created earlier. The pull
// request id rides inside the comment id, since Bitbucket addresses a
// comment through its pull request and the interface passes only the id.
func (p *Provider) UpdateComment(ctx context.Context, repoPath, commentID, body string) error {
	number, id, err := splitCommentID(commentID)
	if err != nil {
		return err
	}
	_, _, err = p.do(ctx, http.MethodPut,
		fmt.Sprintf("%s/repositories/%s/pullrequests/%d/comments/%s", p.apiURL, repoPath, number, id),
		map[string]any{"content": map[string]any{"raw": body}})
	return err
}

// DeleteComment removes a comment SyncerD created earlier.
func (p *Provider) DeleteComment(ctx context.Context, repoPath, commentID string) error {
	number, id, err := splitCommentID(commentID)
	if err != nil {
		return err
	}
	_, _, err = p.do(ctx, http.MethodDelete,
		fmt.Sprintf("%s/repositories/%s/pullrequests/%d/comments/%s", p.apiURL, repoPath, number, id), nil)
	return err
}

// CreateReviewComment posts a comment anchored to a line of the diff.
// Bitbucket anchors by path and line alone, with no commit SHA.
func (p *Provider) CreateReviewComment(ctx context.Context, repoPath string, number int, rc vcs.ReviewComment) (string, error) {
	if rc.Path == "" {
		return "", fmt.Errorf("%w: bitbucket needs a file path", vcs.ErrAnchorRejected)
	}

	inline := map[string]any{"path": rc.Path}
	if rc.Line > 0 {
		if rc.Side == "LEFT" {
			inline["from"] = rc.Line
		} else {
			inline["to"] = rc.Line
		}
	}

	id, err := p.postComment(ctx, repoPath, number, map[string]any{
		"content": map[string]any{"raw": rc.Body},
		"inline":  inline,
	})
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusBadRequest {
			return "", fmt.Errorf("%w: %s:%d", vcs.ErrAnchorRejected, rc.Path, rc.Line)
		}
		return "", err
	}
	return id, nil
}

// postComment posts a comment payload and returns the packed comment id.
func (p *Provider) postComment(ctx context.Context, repoPath string, number int, payload map[string]any) (string, error) {
	raw, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repositories/%s/pullrequests/%d/comments", p.apiURL, repoPath, number), payload)
	if err != nil {
		return "", err
	}
	var created apiComment
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("bitbucket: decode created comment: %w", err)
	}
	return joinCommentID(number, strconv.FormatInt(created.ID, 10)), nil
}

// joinCommentID packs the pull request id into the comment id.
func joinCommentID(number int, id string) string {
	return strconv.Itoa(number) + "/" + id
}

// splitCommentID unpacks what joinCommentID produced.
func splitCommentID(commentID string) (int, string, error) {
	prID, id, ok := strings.Cut(commentID, "/")
	if !ok {
		return 0, "", fmt.Errorf("bitbucket: malformed comment id %q, want <pull request id>/<comment id>", commentID)
	}
	number, err := strconv.Atoi(prID)
	if err != nil {
		return 0, "", fmt.Errorf("bitbucket: malformed comment id %q: %w", commentID, err)
	}
	return number, id, nil
}
