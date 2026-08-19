package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// apiComment covers both discussion comments and review comments; the
// anchor fields are simply absent on the former.
type apiComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	CommitID  string `json:"commit_id"`
	InReplyTo int64  `json:"in_reply_to_id"`
}

func (a apiComment) toComment() vcs.Comment {
	return vcs.Comment{
		SourceID:  strconv.FormatInt(a.ID, 10),
		Author:    vcs.Actor{Handle: a.User.Login},
		Body:      a.Body,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

func (a apiComment) toReviewComment() vcs.ReviewComment {
	rc := vcs.ReviewComment{
		Comment:   a.toComment(),
		Path:      a.Path,
		Line:      a.Line,
		Side:      a.Side,
		CommitSHA: a.CommitID,
	}
	if a.InReplyTo != 0 {
		rc.InReplyTo = strconv.FormatInt(a.InReplyTo, 10)
	}
	return rc
}

// apiReview is a review verdict.
type apiReview struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

func (a apiReview) toReview() vcs.Review {
	return vcs.Review{
		SourceID:    strconv.FormatInt(a.ID, 10),
		Author:      vcs.Actor{Handle: a.User.Login},
		Body:        a.Body,
		State:       strings.ToLower(a.State),
		SubmittedAt: a.SubmittedAt,
	}
}

// ListComments returns the discussion comments on a pull request. GitHub
// serves these from the issues endpoint, since a pull request is an issue
// with a diff attached.
func (p *Provider) ListComments(ctx context.Context, repoPath string, number int) ([]vcs.Comment, error) {
	raw, err := p.listCommentPages(ctx, fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100", p.apiURL, repoPath, number))
	if err != nil {
		return nil, err
	}
	out := make([]vcs.Comment, 0, len(raw))
	for _, a := range raw {
		out = append(out, a.toComment())
	}
	return out, nil
}

// ListReviewComments returns the comments anchored to lines of the diff.
//
// The pull request itself is read first for its base SHA. GitHub does not
// report one per comment and does not need one, but GitLab refuses to
// anchor a comment without base, head, and start SHAs, and CodeCommit needs
// a before commit id. Without this, every inline comment mirrored from
// GitHub to either of them would silently downgrade to a plain discussion
// comment.
func (p *Provider) ListReviewComments(ctx context.Context, repoPath string, number int) ([]vcs.ReviewComment, error) {
	raw, err := p.listCommentPages(ctx, fmt.Sprintf("%s/repos/%s/pulls/%d/comments?per_page=100", p.apiURL, repoPath, number))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}

	baseSHA, err := p.baseSHA(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}

	out := make([]vcs.ReviewComment, 0, len(raw))
	for _, a := range raw {
		rc := a.toReviewComment()
		rc.BaseSHA = baseSHA
		out = append(out, rc)
	}
	return out, nil
}

// baseSHA reports the commit a pull request's diff is measured from.
func (p *Provider) baseSHA(ctx context.Context, repoPath string, number int) (string, error) {
	body, _, err := p.do(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/pulls/%d", p.apiURL, repoPath, number), nil)
	if err != nil {
		return "", err
	}
	var pr struct {
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return "", fmt.Errorf("github: decode pull request for its base SHA: %w", err)
	}
	return pr.Base.SHA, nil
}

// ListReviews returns the review verdicts on a pull request.
func (p *Provider) ListReviews(ctx context.Context, repoPath string, number int) ([]vcs.Review, error) {
	pageURL := fmt.Sprintf("%s/repos/%s/pulls/%d/reviews?per_page=100", p.apiURL, repoPath, number)

	var out []vcs.Review
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("github: review pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("github: review pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, header, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}
		var page []apiReview
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("github: decode review list: %w", err)
		}
		for _, a := range page {
			// A pending review has not been submitted and is visible only
			// to its author, so it is not part of the conversation to
			// mirror.
			if strings.EqualFold(a.State, "pending") {
				continue
			}
			out = append(out, a.toReview())
		}

		next, nerr := resolveNext(pageURL, header.Get("Link"))
		if nerr != nil {
			return out, nerr
		}
		pageURL = next
	}
	return out, nil
}

// listCommentPages walks a paginated comment listing.
func (p *Provider) listCommentPages(ctx context.Context, pageURL string) ([]apiComment, error) {
	var out []apiComment
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("github: comment pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("github: comment pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, header, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}
		var page []apiComment
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("github: decode comment list: %w", err)
		}
		out = append(out, page...)

		next, nerr := resolveNext(pageURL, header.Get("Link"))
		if nerr != nil {
			return out, nerr
		}
		pageURL = next
	}
	return out, nil
}

// CreateComment posts a discussion comment and returns its destination id.
func (p *Provider) CreateComment(ctx context.Context, repoPath string, number int, body string) (string, error) {
	raw, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/issues/%d/comments", p.apiURL, repoPath, number),
		map[string]any{"body": body})
	if err != nil {
		return "", err
	}
	var created apiComment
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("github: decode created comment: %w", err)
	}
	return strconv.FormatInt(created.ID, 10), nil
}

// UpdateComment rewrites a comment SyncerD created earlier.
func (p *Provider) UpdateComment(ctx context.Context, repoPath, commentID, body string) error {
	_, _, err := p.do(ctx, http.MethodPatch,
		fmt.Sprintf("%s/repos/%s/issues/comments/%s", p.apiURL, repoPath, commentID),
		map[string]any{"body": body})
	return err
}

// DeleteComment removes a comment SyncerD created earlier.
func (p *Provider) DeleteComment(ctx context.Context, repoPath, commentID string) error {
	_, _, err := p.do(ctx, http.MethodDelete,
		fmt.Sprintf("%s/repos/%s/issues/comments/%s", p.apiURL, repoPath, commentID), nil)
	return err
}

// CreateReviewComment posts a comment anchored to a line of the diff.
//
// The anchor uses the source's own commit SHA, which the destination has,
// because the ref mirror and the pull request branch both carry it. A
// destination that still refuses the anchor, which it does when the line is
// outside its view of the diff, answers 422; that is returned as
// vcs.ErrAnchorRejected so the caller can downgrade to a discussion comment
// rather than drop the remark.
func (p *Provider) CreateReviewComment(ctx context.Context, repoPath string, number int, rc vcs.ReviewComment) (string, error) {
	payload := map[string]any{
		"body":      rc.Body,
		"commit_id": rc.CommitSHA,
		"path":      rc.Path,
	}
	if rc.Line > 0 {
		payload["line"] = rc.Line
	}
	if rc.Side != "" {
		payload["side"] = rc.Side
	}

	raw, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/pulls/%d/comments", p.apiURL, repoPath, number), payload)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusUnprocessableEntity {
			return "", fmt.Errorf("%w: %s:%d", vcs.ErrAnchorRejected, rc.Path, rc.Line)
		}
		return "", err
	}

	var created apiComment
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("github: decode created review comment: %w", err)
	}
	return strconv.FormatInt(created.ID, 10), nil
}
