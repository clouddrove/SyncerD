package gitlab

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

// draftPrefix is how GitLab expresses a draft merge request.
//
// GitLab has no writable draft field: `draft` on the response is read only,
// and a merge request is a draft precisely because its title begins with
// one of the recognised prefixes. Mirroring a draft therefore means
// changing the title, which is the one place SyncerD does not copy a title
// verbatim.
const draftPrefix = "Draft: "

// draftPrefixes are the forms GitLab recognises, used when stripping.
var draftPrefixes = []string{"Draft: ", "Draft:", "[Draft] ", "[Draft]", "(Draft) ", "(Draft)"}

// apiMergeRequest is the subset of the GitLab merge request object SyncerD
// reads.
type apiMergeRequest struct {
	IID          int      `json:"iid"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	State        string   `json:"state"`
	Draft        bool     `json:"draft"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	WebURL       string   `json:"web_url"`
	Labels       []string `json:"labels"`
	Author       struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"author"`
	SHA          string     `json:"sha"`
	MergeCommit  string     `json:"merge_commit_sha"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	MergedAt     *time.Time `json:"merged_at"`
	DiffRefs     *diffRefs  `json:"diff_refs"`
	SourceProjID int        `json:"source_project_id"`
	TargetProjID int        `json:"target_project_id"`
}

// diffRefs carries the three SHAs GitLab requires to anchor a comment.
type diffRefs struct {
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

func (a apiMergeRequest) toPullRequest() vcs.PullRequest {
	pr := vcs.PullRequest{
		Number:     a.IID,
		Title:      a.Title,
		Body:       a.Description,
		State:      mergeRequestState(a),
		Draft:      a.Draft,
		Author:     vcs.Actor{Handle: a.Author.Username, DisplayName: a.Author.Name},
		HeadBranch: a.SourceBranch,
		HeadSHA:    a.SHA,
		BaseBranch: a.TargetBranch,
		Labels:     a.Labels,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
		ClosedAt:   a.ClosedAt,
		MergedAt:   a.MergedAt,
		MergeSHA:   a.MergeCommit,
		WebURL:     a.WebURL,
	}
	// A merge request opened from a fork has a different source project.
	// The clone URL is not on this object, so HeadRepoCloneURL stays empty
	// and the engine skips fetching that head with a warning rather than
	// fetching from the wrong repository. Same project heads, the common
	// case, arrive through the ordinary branch mirror.
	return pr
}

// mergeRequestState maps GitLab's four states onto the three state model.
func mergeRequestState(a apiMergeRequest) vcs.PRState {
	switch a.State {
	case "merged":
		return vcs.PRMerged
	case "closed":
		return vcs.PRClosed
	case "locked":
		// Transient: a merge request sits in locked while a merge is in
		// flight. Reading it as closed would close the destination and post
		// "closed without merging" moments before it becomes merged, and
		// nothing would ever correct that note.
		return vcs.PROpen
	default:
		return vcs.PROpen
	}
}

// projectRef is the URL encoded project path a merge request lives under.
func (p *Provider) projectRef(repoPath string) string {
	return url.PathEscape(strings.Trim(repoPath, "/"))
}

// ListPullRequests returns the open merge requests of one project.
func (p *Provider) ListPullRequests(ctx context.Context, repoPath string, opts vcs.PRListOptions) ([]vcs.PullRequest, error) {
	for _, s := range opts.States {
		if s != vcs.PROpen {
			return nil, fmt.Errorf("gitlab: listing %s merge requests is not supported", s)
		}
	}
	return p.listMergeRequests(ctx, repoPath, "state=opened")
}

// FindPullRequest locates a merge request by its source branch.
func (p *Provider) FindPullRequest(ctx context.Context, repoPath, headBranch string) (vcs.PullRequest, bool, error) {
	found, err := p.listMergeRequests(ctx, repoPath, "state=all&source_branch="+url.QueryEscape(headBranch))
	if err != nil {
		return vcs.PullRequest{}, false, err
	}
	if len(found) == 0 {
		return vcs.PullRequest{}, false, nil
	}
	return found[0], true, nil
}

// listMergeRequests walks a paginated merge request listing.
func (p *Provider) listMergeRequests(ctx context.Context, repoPath, query string) ([]vcs.PullRequest, error) {
	var out []vcs.PullRequest
	page := "1"
	seen := make(map[string]bool)

	for page != "" {
		if seen[page] {
			return out, fmt.Errorf("gitlab: merge request pagination revisited page %q, refusing to loop", page)
		}
		if len(seen) >= maxPages {
			return out, fmt.Errorf("gitlab: merge request pagination exceeded %d pages, refusing to continue", maxPages)
		}
		seen[page] = true

		endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?%s&per_page=100&order_by=updated_at&page=%s",
			p.base, p.projectRef(repoPath), query, url.QueryEscape(page))

		body, header, err := p.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return out, err
		}

		var page1 []apiMergeRequest
		if err := json.Unmarshal(body, &page1); err != nil {
			return out, fmt.Errorf("gitlab: decode merge request list: %w", err)
		}
		for _, a := range page1 {
			out = append(out, a.toPullRequest())
		}

		page = strings.TrimSpace(header.Get("X-Next-Page"))
	}
	return out, nil
}

// CreatePullRequest opens a merge request.
func (p *Provider) CreatePullRequest(ctx context.Context, repoPath string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	payload := map[string]any{
		"source_branch": spec.HeadBranch,
		"target_branch": spec.BaseBranch,
		"title":         draftTitle(spec.Title, spec.Draft),
		"description":   spec.Body,
	}
	if len(spec.Labels) > 0 {
		payload["labels"] = strings.Join(spec.Labels, ",")
	}

	body, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", p.base, p.projectRef(repoPath)), payload)
	if err != nil {
		return vcs.PullRequest{}, err
	}

	var created apiMergeRequest
	if err := json.Unmarshal(body, &created); err != nil {
		return vcs.PullRequest{}, fmt.Errorf("gitlab: decode created merge request: %w", err)
	}
	return created.toPullRequest(), nil
}

// UpdatePullRequest rewrites the mutable fields of a merge request.
func (p *Provider) UpdatePullRequest(ctx context.Context, repoPath string, number int, spec vcs.PullRequestSpec) error {
	payload := map[string]any{
		"title":         draftTitle(spec.Title, spec.Draft),
		"description":   spec.Body,
		"target_branch": spec.BaseBranch,
	}
	// An empty labels string clears them, which is right when the mirror
	// owns labels: a label removed at the source is removed here. With
	// label mirroring off, the field is omitted so labels somebody added at
	// the destination survive.
	if spec.SyncLabels {
		payload["labels"] = strings.Join(spec.Labels, ",")
	}

	_, _, err := p.do(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", p.base, p.projectRef(repoPath), number), payload)
	return err
}

// ClosePullRequest closes a merge request. It is never merged here, for the
// reason given on vcs.PullRequestWriter.
func (p *Provider) ClosePullRequest(ctx context.Context, repoPath string, number int) error {
	return p.setStateEvent(ctx, repoPath, number, "close")
}

// ReopenPullRequest moves a closed merge request back to open.
func (p *Provider) ReopenPullRequest(ctx context.Context, repoPath string, number int) error {
	return p.setStateEvent(ctx, repoPath, number, "reopen")
}

func (p *Provider) setStateEvent(ctx context.Context, repoPath string, number int, event string) error {
	_, _, err := p.do(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", p.base, p.projectRef(repoPath), number),
		map[string]any{"state_event": event})
	return err
}

// draftTitle applies or strips GitLab's draft prefix, since that is the only
// way to express draft state on this provider.
func draftTitle(title string, draft bool) string {
	stripped := stripDraft(title)
	if draft {
		return draftPrefix + stripped
	}
	return stripped
}

// stripDraft removes any recognised draft prefix from a title.
func stripDraft(title string) string {
	for {
		trimmed := title
		for _, prefix := range draftPrefixes {
			if len(title) >= len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
				trimmed = strings.TrimSpace(title[len(prefix):])
				break
			}
		}
		if trimmed == title {
			return title
		}
		title = trimmed
	}
}

// apiNote is a GitLab note, which is what every comment is.
type apiNote struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Position  *struct {
		NewPath  string `json:"new_path"`
		OldPath  string `json:"old_path"`
		NewLine  int    `json:"new_line"`
		OldLine  int    `json:"old_line"`
		BaseSHA  string `json:"base_sha"`
		HeadSHA  string `json:"head_sha"`
		StartSHA string `json:"start_sha"`
	} `json:"position"`
}

func (a apiNote) toComment() vcs.Comment {
	return vcs.Comment{
		SourceID:  strconv.FormatInt(a.ID, 10),
		Author:    vcs.Actor{Handle: a.Author.Username, DisplayName: a.Author.Name},
		Body:      a.Body,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

// ListComments returns the discussion notes on a merge request.
//
// System notes are skipped. GitLab writes those itself for events like
// "changed the description", and mirroring them would fill the destination
// with a second, redundant activity log.
func (p *Provider) ListComments(ctx context.Context, repoPath string, number int) ([]vcs.Comment, error) {
	notes, err := p.listNotes(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}
	out := make([]vcs.Comment, 0, len(notes))
	for _, n := range notes {
		if n.System || n.Position != nil {
			continue
		}
		out = append(out, n.toComment())
	}
	return out, nil
}

// ListReviewComments returns the notes anchored to a line of the diff.
func (p *Provider) ListReviewComments(ctx context.Context, repoPath string, number int) ([]vcs.ReviewComment, error) {
	notes, err := p.listNotes(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}
	out := make([]vcs.ReviewComment, 0)
	for _, n := range notes {
		if n.System || n.Position == nil {
			continue
		}
		rc := vcs.ReviewComment{
			Comment:   n.toComment(),
			Path:      n.Position.NewPath,
			Line:      n.Position.NewLine,
			Side:      "RIGHT",
			CommitSHA: n.Position.HeadSHA,
			BaseSHA:   n.Position.BaseSHA,
		}
		// A comment on a deleted line reports new_line as null and old_line
		// as set. new_path is populated on both sides of a text diff, so it
		// cannot be used to tell them apart.
		if n.Position.NewLine == 0 && n.Position.OldLine != 0 {
			rc.Path = n.Position.OldPath
			rc.Line = n.Position.OldLine
			rc.Side = "LEFT"
		}
		out = append(out, rc)
	}
	return out, nil
}

// ListReviews returns approvals as review verdicts.
//
// GitLab's REST API has no "request changes" verdict: that state exists only
// in the UI and GraphQL. Approvals are all this can report, and P2 mirrors
// them as attributed text rather than as real approvals anyway.
func (p *Provider) ListReviews(ctx context.Context, repoPath string, number int) ([]vcs.Review, error) {
	body, _, err := p.do(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/approvals", p.base, p.projectRef(repoPath), number), nil)
	if err != nil {
		return nil, err
	}

	var approvals struct {
		ApprovedBy []struct {
			User struct {
				ID       int    `json:"id"`
				Username string `json:"username"`
				Name     string `json:"name"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if err := json.Unmarshal(body, &approvals); err != nil {
		return nil, fmt.Errorf("gitlab: decode approvals: %w", err)
	}

	out := make([]vcs.Review, 0, len(approvals.ApprovedBy))
	for _, a := range approvals.ApprovedBy {
		out = append(out, vcs.Review{
			SourceID: strconv.Itoa(a.User.ID),
			Author:   vcs.Actor{Handle: a.User.Username, DisplayName: a.User.Name},
			State:    "approved",
		})
	}
	return out, nil
}

// listNotes walks the paginated note listing for one merge request.
func (p *Provider) listNotes(ctx context.Context, repoPath string, number int) ([]apiNote, error) {
	var out []apiNote
	page := "1"
	seen := make(map[string]bool)

	for page != "" {
		if seen[page] {
			return out, fmt.Errorf("gitlab: note pagination revisited page %q, refusing to loop", page)
		}
		if len(seen) >= maxPages {
			return out, fmt.Errorf("gitlab: note pagination exceeded %d pages, refusing to continue", maxPages)
		}
		seen[page] = true

		endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes?per_page=100&page=%s",
			p.base, p.projectRef(repoPath), number, url.QueryEscape(page))

		body, header, err := p.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return out, err
		}
		var notes []apiNote
		if err := json.Unmarshal(body, &notes); err != nil {
			return out, fmt.Errorf("gitlab: decode notes: %w", err)
		}
		out = append(out, notes...)

		page = strings.TrimSpace(header.Get("X-Next-Page"))
	}
	return out, nil
}

// CreateComment posts a discussion note.
func (p *Provider) CreateComment(ctx context.Context, repoPath string, number int, body string) (string, error) {
	raw, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes", p.base, p.projectRef(repoPath), number),
		map[string]any{"body": body})
	if err != nil {
		return "", err
	}
	var created apiNote
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("gitlab: decode created note: %w", err)
	}
	return joinNoteID(number, strconv.FormatInt(created.ID, 10)), nil
}

// UpdateComment rewrites a note SyncerD created earlier.
//
// GitLab scopes a note to its merge request, so the merge request iid is
// part of the path. It is carried in the comment id as "<iid>/<noteID>"
// because the interface passes only the id back on update and delete.
func (p *Provider) UpdateComment(ctx context.Context, repoPath, commentID, body string) error {
	number, noteID, err := splitNoteID(commentID)
	if err != nil {
		return err
	}
	_, _, err = p.do(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes/%s", p.base, p.projectRef(repoPath), number, noteID),
		map[string]any{"body": body})
	return err
}

// DeleteComment removes a note SyncerD created earlier.
func (p *Provider) DeleteComment(ctx context.Context, repoPath, commentID string) error {
	number, noteID, err := splitNoteID(commentID)
	if err != nil {
		return err
	}
	_, _, err = p.do(ctx, http.MethodDelete,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes/%s", p.base, p.projectRef(repoPath), number, noteID), nil)
	return err
}

// CreateReviewComment posts a note anchored to a line of the diff.
//
// GitLab refuses a position without all three of base, head, and start SHA,
// so a source that did not report them cannot be anchored here. That is
// reported as vcs.ErrAnchorRejected, which the caller already handles by
// downgrading to a discussion comment.
func (p *Provider) CreateReviewComment(ctx context.Context, repoPath string, number int, rc vcs.ReviewComment) (string, error) {
	if rc.CommitSHA == "" || rc.BaseSHA == "" {
		return "", fmt.Errorf("%w: gitlab needs base and head SHAs to place a comment on %s:%d", vcs.ErrAnchorRejected, rc.Path, rc.Line)
	}

	position := map[string]any{
		"base_sha":      rc.BaseSHA,
		"head_sha":      rc.CommitSHA,
		"start_sha":     rc.BaseSHA,
		"position_type": "text",
		"new_path":      rc.Path,
		"old_path":      rc.Path,
	}
	if rc.Side == "LEFT" {
		position["old_line"] = rc.Line
	} else {
		position["new_line"] = rc.Line
	}

	raw, _, err := p.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/discussions", p.base, p.projectRef(repoPath), number),
		map[string]any{"body": rc.Body, "position": position})
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && (he.status == http.StatusBadRequest || he.status == http.StatusUnprocessableEntity) {
			return "", fmt.Errorf("%w: %s:%d", vcs.ErrAnchorRejected, rc.Path, rc.Line)
		}
		return "", err
	}

	var created struct {
		Notes []apiNote `json:"notes"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("gitlab: decode created discussion: %w", err)
	}
	if len(created.Notes) == 0 {
		return "", fmt.Errorf("gitlab: created discussion carried no note")
	}
	return joinNoteID(number, strconv.FormatInt(created.Notes[0].ID, 10)), nil
}

// joinNoteID packs the merge request iid into the comment id, since GitLab
// needs both to address a note and the interface carries only the id.
func joinNoteID(number int, noteID string) string {
	return strconv.Itoa(number) + "/" + noteID
}

// splitNoteID unpacks what joinNoteID produced.
func splitNoteID(commentID string) (int, string, error) {
	iid, noteID, ok := strings.Cut(commentID, "/")
	if !ok {
		return 0, "", fmt.Errorf("gitlab: malformed comment id %q, want <mr iid>/<note id>", commentID)
	}
	number, err := strconv.Atoi(iid)
	if err != nil {
		return 0, "", fmt.Errorf("gitlab: malformed comment id %q: %w", commentID, err)
	}
	return number, noteID, nil
}
