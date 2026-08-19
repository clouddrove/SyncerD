package azuredevops

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

// Azure DevOps spells the route segment differently depending on which
// resource it addresses: lower case "pullrequests" for the pull request
// routes themselves, camel case "pullRequests" for threads, labels, and
// reviewers. This is documented Microsoft behaviour, not a typo here, and
// the two constants exist so it cannot be quietly "fixed" into a 404.
const (
	prRoute       = "pullrequests"
	prChildRoute  = "pullRequests"
	refHeadPrefix = "refs/heads/"
)

// apiPullRequest is the subset of the Azure DevOps pull request object
// SyncerD reads.
type apiPullRequest struct {
	PullRequestID int    `json:"pullRequestId"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Status        string `json:"status"`
	IsDraft       bool   `json:"isDraft"`
	SourceRefName string `json:"sourceRefName"`
	TargetRefName string `json:"targetRefName"`
	CreatedBy     struct {
		UniqueName  string `json:"uniqueName"`
		DisplayName string `json:"displayName"`
	} `json:"createdBy"`
	CreationDate time.Time  `json:"creationDate"`
	ClosedDate   *time.Time `json:"closedDate"`
	LastMergeSHA *struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	MergeCommit *struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeCommit"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Repository struct {
		Name    string `json:"name"`
		WebURL  string `json:"webUrl"`
		Project struct {
			Name string `json:"name"`
		} `json:"project"`
	} `json:"repository"`
	ForkSource *struct {
		Repository struct {
			RemoteURL string `json:"remoteUrl"`
		} `json:"repository"`
	} `json:"forkSource"`
}

func (a apiPullRequest) toPullRequest() vcs.PullRequest {
	pr := vcs.PullRequest{
		Number:     a.PullRequestID,
		Title:      a.Title,
		Body:       a.Description,
		State:      pullRequestState(a),
		Draft:      a.IsDraft,
		Author:     vcs.Actor{Handle: a.CreatedBy.UniqueName, DisplayName: a.CreatedBy.DisplayName},
		HeadBranch: strings.TrimPrefix(a.SourceRefName, refHeadPrefix),
		BaseBranch: strings.TrimPrefix(a.TargetRefName, refHeadPrefix),
		CreatedAt:  a.CreationDate,
		ClosedAt:   a.ClosedDate,
	}
	// UpdatedAt is deliberately left zero. Azure DevOps reports no update
	// timestamp on a pull request, and standing the creation date in for
	// one would be worse than reporting nothing: the creation date never
	// changes, so the engine's watermark would skip the pull request
	// forever after the first run and no later edit, comment, or verdict
	// would ever be mirrored. A zero time tells the engine there is no
	// watermark to use, and it reconciles every run instead.
	if a.LastMergeSHA != nil {
		pr.HeadSHA = a.LastMergeSHA.CommitID
	}
	if a.MergeCommit != nil && pr.State == vcs.PRMerged {
		pr.MergeSHA = a.MergeCommit.CommitID
	}
	for _, l := range a.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	if a.ForkSource != nil && a.ForkSource.Repository.RemoteURL != "" {
		pr.HeadRepoCloneURL = vcs.SanitizeCloneURL(a.ForkSource.Repository.RemoteURL)
	}
	if a.Repository.WebURL != "" {
		pr.WebURL = fmt.Sprintf("%s/pullrequest/%d", a.Repository.WebURL, a.PullRequestID)
	}
	return pr
}

// pullRequestState maps Azure DevOps status onto the three state model.
func pullRequestState(a apiPullRequest) vcs.PRState {
	switch strings.ToLower(a.Status) {
	case "completed":
		return vcs.PRMerged
	case "abandoned":
		return vcs.PRClosed
	default:
		return vcs.PROpen
	}
}

// prListResponse is the envelope around a pull request listing.
type prListResponse struct {
	Value []apiPullRequest `json:"value"`
	Count int              `json:"count"`
}

// pageSize bounds one page of a $top/$skip walk. Azure DevOps offers no
// continuation token on pull requests, so paging is by offset.
const pageSize = 100

// ListPullRequests returns the active pull requests of one repository.
func (p *Provider) ListPullRequests(ctx context.Context, repoPath string, opts vcs.PRListOptions) ([]vcs.PullRequest, error) {
	for _, s := range opts.States {
		if s != vcs.PROpen {
			return nil, fmt.Errorf("azuredevops: listing %s pull requests is not supported", s)
		}
	}
	return p.listPullRequests(ctx, repoPath, "searchCriteria.status=active")
}

// FindPullRequest locates a pull request by its source branch.
func (p *Provider) FindPullRequest(ctx context.Context, repoPath, headBranch string) (vcs.PullRequest, bool, error) {
	query := "searchCriteria.status=all&searchCriteria.sourceRefName=" + url.QueryEscape(refHeadPrefix+headBranch)
	found, err := p.listPullRequests(ctx, repoPath, query)
	if err != nil {
		return vcs.PullRequest{}, false, err
	}
	if len(found) == 0 {
		return vcs.PullRequest{}, false, nil
	}
	return found[0], true, nil
}

// listPullRequests walks a listing with $top and $skip.
func (p *Provider) listPullRequests(ctx context.Context, repoPath, query string) ([]vcs.PullRequest, error) {
	var out []vcs.PullRequest

	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("azuredevops: pull request pagination exceeded %d pages, refusing to continue", maxPages)
		}

		endpoint := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/%s?%s&$top=%d&$skip=%d&api-version=%s",
			p.apiURL, url.PathEscape(p.org), url.PathEscape(p.project), url.PathEscape(repoPath),
			prRoute, query, pageSize, pages*pageSize, apiVersion)

		body, _, err := p.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return out, err
		}
		var page prListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("azuredevops: decode pull request list: %w", err)
		}
		for _, a := range page.Value {
			out = append(out, a.toPullRequest())
		}
		// A short page is the last page: there is no continuation token to
		// tell us otherwise.
		if len(page.Value) < pageSize {
			return out, nil
		}
	}
}

// CreatePullRequest opens a pull request. Branch names are sent as full
// refs, which is what this API expects.
func (p *Provider) CreatePullRequest(ctx context.Context, repoPath string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	payload := map[string]any{
		"sourceRefName": refHeadPrefix + spec.HeadBranch,
		"targetRefName": refHeadPrefix + spec.BaseBranch,
		"title":         spec.Title,
		"description":   truncateDescription(spec.Body),
		"isDraft":       spec.Draft,
	}

	body, _, err := p.do(ctx, http.MethodPost, p.prURL(repoPath, prRoute, ""), payload)
	if err != nil {
		return vcs.PullRequest{}, err
	}

	var created apiPullRequest
	if err := json.Unmarshal(body, &created); err != nil {
		return vcs.PullRequest{}, fmt.Errorf("azuredevops: decode created pull request: %w", err)
	}

	pr := created.toPullRequest()
	for _, label := range spec.Labels {
		if err := p.addLabel(ctx, repoPath, pr.Number, label); err != nil {
			return pr, fmt.Errorf("azuredevops: pull request %d created but label %q failed: %w", pr.Number, label, err)
		}
	}
	return pr, nil
}

// UpdatePullRequest rewrites title and description.
//
// Azure DevOps documents a closed set of updatable properties: status,
// title, description, completion and merge options, auto complete, and the
// target ref. isDraft appears in the request schema but not in that list,
// so it is not sent on update; a pull request that leaves draft at the
// source keeps its draft flag here rather than failing the update.
func (p *Provider) UpdatePullRequest(ctx context.Context, repoPath string, number int, spec vcs.PullRequestSpec) error {
	payload := map[string]any{
		"title":         spec.Title,
		"description":   truncateDescription(spec.Body),
		"targetRefName": refHeadPrefix + spec.BaseBranch,
	}
	if _, _, err := p.do(ctx, http.MethodPatch, p.prURL(repoPath, prRoute, strconv.Itoa(number)), payload); err != nil {
		return err
	}

	if spec.SyncLabels {
		if err := p.reconcileLabels(ctx, repoPath, number, spec.Labels); err != nil {
			return fmt.Errorf("azuredevops: pull request %d updated but labels failed: %w", number, err)
		}
	}
	return nil
}

// reconcileLabels makes the destination labels match the source exactly.
//
// Adding without removing would leave a label deleted at the source in
// place at the destination forever, which contradicts the rule that the
// source is the authority. Re-adding a label that is already there is also
// avoided: it is a wasted call per label per run, and its failure would
// fail the whole pull request.
func (p *Provider) reconcileLabels(ctx context.Context, repoPath string, number int, want []string) error {
	current, err := p.listLabels(ctx, repoPath, number)
	if err != nil {
		return err
	}

	wanted := make(map[string]bool, len(want))
	for _, l := range want {
		wanted[l] = true
		if !current[l] {
			if err := p.addLabel(ctx, repoPath, number, l); err != nil {
				return err
			}
		}
	}
	for l := range current {
		if !wanted[l] {
			if err := p.removeLabel(ctx, repoPath, number, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// listLabels reports the labels currently on a pull request.
func (p *Provider) listLabels(ctx context.Context, repoPath string, number int) (map[string]bool, error) {
	body, _, err := p.do(ctx, http.MethodGet,
		p.prURL(repoPath, prChildRoute, strconv.Itoa(number)+"/labels"), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("azuredevops: decode labels: %w", err)
	}
	out := make(map[string]bool, len(resp.Value))
	for _, l := range resp.Value {
		out[l.Name] = true
	}
	return out, nil
}

// removeLabel detaches a label. This removes the assignment, not the tag
// definition, which other pull requests may still use.
func (p *Provider) removeLabel(ctx context.Context, repoPath string, number int, name string) error {
	_, _, err := p.do(ctx, http.MethodDelete,
		p.prURL(repoPath, prChildRoute, strconv.Itoa(number)+"/labels/"+url.PathEscape(name)), nil)
	return err
}

// truncateDescription keeps a body inside the documented 4000 character
// limit. A mirrored body that would be rejected outright is worse than one
// that says where to read the rest, and the marker and attribution header
// sit at the top, so what is cut is the tail of the original text.
func truncateDescription(body string) string {
	const limit = 4000
	const notice = "\n\n[truncated by SyncerD: Azure DevOps limits a description to 4000 characters]"

	// The limit counts characters, not bytes, and slicing a string by byte
	// offset would both cut a mirrored body far short of the real limit and
	// risk splitting a multi byte rune.
	runes := []rune(body)
	if len(runes) <= limit {
		return body
	}
	keep := limit - len([]rune(notice))
	return string(runes[:keep]) + notice
}

// ClosePullRequest abandons a pull request, which is Azure DevOps' close.
func (p *Provider) ClosePullRequest(ctx context.Context, repoPath string, number int) error {
	return p.setStatus(ctx, repoPath, number, "abandoned")
}

// ReopenPullRequest moves an abandoned pull request back to active.
func (p *Provider) ReopenPullRequest(ctx context.Context, repoPath string, number int) error {
	return p.setStatus(ctx, repoPath, number, "active")
}

func (p *Provider) setStatus(ctx context.Context, repoPath string, number int, status string) error {
	_, _, err := p.do(ctx, http.MethodPatch,
		p.prURL(repoPath, prRoute, strconv.Itoa(number)), map[string]any{"status": status})
	return err
}

// addLabel attaches one label, which Azure DevOps models as a tag.
func (p *Provider) addLabel(ctx context.Context, repoPath string, number int, name string) error {
	_, _, err := p.do(ctx, http.MethodPost,
		p.prURL(repoPath, prChildRoute, strconv.Itoa(number)+"/labels"), map[string]any{"name": name})
	return err
}

// prURL builds a pull request route. route selects the casing Azure DevOps
// requires for that family of endpoints; see the constants above.
func (p *Provider) prURL(repoPath, route, suffix string) string {
	base := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/%s",
		p.apiURL, url.PathEscape(p.org), url.PathEscape(p.project), url.PathEscape(repoPath), route)
	if suffix != "" {
		base += "/" + suffix
	}
	return base + "?api-version=" + apiVersion
}

// apiThread is a comment thread. Azure DevOps has no free standing comment:
// every comment belongs to a thread, and an inline comment is a thread with
// a threadContext.
type apiThread struct {
	ID            int          `json:"id"`
	Comments      []apiComment `json:"comments"`
	Status        string       `json:"status"`
	ThreadContext *struct {
		FilePath       string      `json:"filePath"`
		RightFileStart *commentPos `json:"rightFileStart"`
		LeftFileStart  *commentPos `json:"leftFileStart"`
	} `json:"threadContext"`
}

// commentPos is Azure DevOps' position: a line and a character offset.
type commentPos struct {
	Line   int `json:"line"`
	Offset int `json:"offset"`
}

// apiComment is one comment inside a thread.
type apiComment struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
	Author  struct {
		UniqueName  string `json:"uniqueName"`
		DisplayName string `json:"displayName"`
	} `json:"author"`
	PublishedDate time.Time `json:"publishedDate"`
	LastUpdated   time.Time `json:"lastUpdatedDate"`
	CommentType   string    `json:"commentType"`
	IsDeleted     bool      `json:"isDeleted"`
	ParentID      int       `json:"parentCommentId"`
}

func (a apiComment) toComment(prNumber, threadID int) vcs.Comment {
	return vcs.Comment{
		SourceID:  joinCommentID(prNumber, threadID, a.ID),
		Author:    vcs.Actor{Handle: a.Author.UniqueName, DisplayName: a.Author.DisplayName},
		Body:      a.Content,
		CreatedAt: a.PublishedDate,
		UpdatedAt: a.LastUpdated,
	}
}

// ListComments returns the discussion comments, which are the comments in
// threads that carry no file anchor.
//
// System threads are skipped: Azure DevOps writes those itself for events
// like a reviewer voting, and mirroring them would duplicate the
// destination's own activity log.
func (p *Provider) ListComments(ctx context.Context, repoPath string, number int) ([]vcs.Comment, error) {
	threads, err := p.listThreads(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}

	var out []vcs.Comment
	for _, th := range threads {
		if th.ThreadContext != nil && th.ThreadContext.FilePath != "" {
			continue
		}
		for _, c := range th.Comments {
			if c.IsDeleted || strings.EqualFold(c.CommentType, "system") {
				continue
			}
			out = append(out, c.toComment(number, th.ID))
		}
	}
	return out, nil
}

// ListReviewComments returns the comments in threads anchored to a file.
func (p *Provider) ListReviewComments(ctx context.Context, repoPath string, number int) ([]vcs.ReviewComment, error) {
	threads, err := p.listThreads(ctx, repoPath, number)
	if err != nil {
		return nil, err
	}

	var out []vcs.ReviewComment
	for _, th := range threads {
		if th.ThreadContext == nil || th.ThreadContext.FilePath == "" {
			continue
		}
		for _, c := range th.Comments {
			if c.IsDeleted || strings.EqualFold(c.CommentType, "system") {
				continue
			}
			rc := vcs.ReviewComment{
				Comment: c.toComment(number, th.ID),
				Path:    th.ThreadContext.FilePath,
				Side:    "RIGHT",
			}
			switch {
			case th.ThreadContext.RightFileStart != nil:
				rc.Line = th.ThreadContext.RightFileStart.Line
			case th.ThreadContext.LeftFileStart != nil:
				rc.Line = th.ThreadContext.LeftFileStart.Line
				rc.Side = "LEFT"
			}
			if c.ParentID != 0 {
				rc.InReplyTo = joinCommentID(number, th.ID, c.ParentID)
			}
			out = append(out, rc)
		}
	}
	return out, nil
}

// ListReviews returns reviewer votes as verdicts.
//
// Azure DevOps expresses a verdict as an integer from -10 to 10 rather than
// a name, so the numbers are mapped onto the words the rest of SyncerD uses.
func (p *Provider) ListReviews(ctx context.Context, repoPath string, number int) ([]vcs.Review, error) {
	body, _, err := p.do(ctx, http.MethodGet,
		p.prURL(repoPath, prChildRoute, strconv.Itoa(number)+"/reviewers"), nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Value []struct {
			ID          string `json:"id"`
			UniqueName  string `json:"uniqueName"`
			DisplayName string `json:"displayName"`
			Vote        int    `json:"vote"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("azuredevops: decode reviewers: %w", err)
	}

	out := make([]vcs.Review, 0, len(resp.Value))
	for _, r := range resp.Value {
		if r.Vote == 0 {
			// No vote cast, so there is no verdict to mirror.
			continue
		}
		out = append(out, vcs.Review{
			SourceID: r.ID,
			Author:   vcs.Actor{Handle: r.UniqueName, DisplayName: r.DisplayName},
			State:    voteState(r.Vote),
		})
	}
	return out, nil
}

// voteState maps an Azure DevOps vote onto the verdict vocabulary.
func voteState(vote int) string {
	switch {
	case vote >= 10:
		return "approved"
	case vote > 0:
		return "approved_with_suggestions"
	case vote <= -10:
		return "changes_requested"
	default:
		return "waiting_for_author"
	}
}

// listThreads reads every thread on a pull request. Azure DevOps returns
// them in one response rather than paging.
func (p *Provider) listThreads(ctx context.Context, repoPath string, number int) ([]apiThread, error) {
	body, _, err := p.do(ctx, http.MethodGet,
		p.prURL(repoPath, prChildRoute, strconv.Itoa(number)+"/threads"), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Value []apiThread `json:"value"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("azuredevops: decode threads: %w", err)
	}
	return resp.Value, nil
}

// CreateComment posts a discussion comment as a new thread.
func (p *Provider) CreateComment(ctx context.Context, repoPath string, number int, body string) (string, error) {
	return p.createThread(ctx, repoPath, number, map[string]any{
		"comments": []map[string]any{{"parentCommentId": 0, "content": body, "commentType": "text"}},
		"status":   "active",
	})
}

// CreateReviewComment posts a comment anchored to a line, as a thread with
// a threadContext.
//
// Azure DevOps anchors by character span rather than by line, so a whole
// line comment runs from the start of the line to the start of the next,
// which is how its own UI represents one.
func (p *Provider) CreateReviewComment(ctx context.Context, repoPath string, number int, rc vcs.ReviewComment) (string, error) {
	if rc.Path == "" {
		return "", fmt.Errorf("%w: azuredevops needs a file path", vcs.ErrAnchorRejected)
	}

	line := rc.Line
	if line < 1 {
		line = 1
	}
	span := map[string]any{
		"filePath": rc.Path,
	}
	start := map[string]any{"line": line, "offset": 1}
	end := map[string]any{"line": line + 1, "offset": 1}
	if rc.Side == "LEFT" {
		span["leftFileStart"], span["leftFileEnd"] = start, end
	} else {
		span["rightFileStart"], span["rightFileEnd"] = start, end
	}

	id, err := p.createThread(ctx, repoPath, number, map[string]any{
		"comments":      []map[string]any{{"parentCommentId": 0, "content": rc.Body, "commentType": "text"}},
		"status":        "active",
		"threadContext": span,
	})
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && (he.status == http.StatusBadRequest || he.status == http.StatusNotFound) {
			return "", fmt.Errorf("%w: %s:%d", vcs.ErrAnchorRejected, rc.Path, rc.Line)
		}
		return "", err
	}
	return id, nil
}

// createThread posts a thread and returns the packed comment id.
func (p *Provider) createThread(ctx context.Context, repoPath string, number int, payload map[string]any) (string, error) {
	raw, _, err := p.do(ctx, http.MethodPost,
		p.prURL(repoPath, prChildRoute, strconv.Itoa(number)+"/threads"), payload)
	if err != nil {
		return "", err
	}
	var created apiThread
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("azuredevops: decode created thread: %w", err)
	}
	if len(created.Comments) == 0 {
		return "", fmt.Errorf("azuredevops: created thread carried no comment")
	}
	return joinCommentID(number, created.ID, created.Comments[0].ID), nil
}

// UpdateComment rewrites a comment SyncerD created earlier.
func (p *Provider) UpdateComment(ctx context.Context, repoPath, commentID, body string) error {
	number, threadID, id, err := splitCommentID(commentID)
	if err != nil {
		return err
	}
	_, _, err = p.do(ctx, http.MethodPatch,
		p.prURL(repoPath, prChildRoute, threadCommentPath(number, threadID, id)),
		map[string]any{"content": body})
	return err
}

// DeleteComment removes a comment SyncerD created earlier.
//
// Azure DevOps deletes softly: the comment stays and is marked deleted, so
// a source deletion leaves a tombstone at the destination rather than
// vanishing. That is the most this provider can do.
func (p *Provider) DeleteComment(ctx context.Context, repoPath, commentID string) error {
	number, threadID, id, err := splitCommentID(commentID)
	if err != nil {
		return err
	}
	_, _, err = p.do(ctx, http.MethodDelete,
		p.prURL(repoPath, prChildRoute, threadCommentPath(number, threadID, id)), nil)
	return err
}

// threadCommentPath addresses one comment: Azure DevOps needs the pull
// request, the thread, and the comment to reach it.
func threadCommentPath(number, threadID, commentID int) string {
	return fmt.Sprintf("%d/threads/%d/comments/%d", number, threadID, commentID)
}

// joinCommentID packs all three ids Azure DevOps needs into the single
// identifier the conversation interface carries.
func joinCommentID(number, threadID, commentID int) string {
	return fmt.Sprintf("%d/%d/%d", number, threadID, commentID)
}

// splitCommentID unpacks what joinCommentID produced.
func splitCommentID(commentID string) (number, threadID, id int, err error) {
	parts := strings.Split(commentID, "/")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("azuredevops: malformed comment id %q, want <pull request id>/<thread id>/<comment id>", commentID)
	}
	for i, dst := range []*int{&number, &threadID, &id} {
		v, cerr := strconv.Atoi(parts[i])
		if cerr != nil {
			return 0, 0, 0, fmt.Errorf("azuredevops: malformed comment id %q: %w", commentID, cerr)
		}
		*dst = v
	}
	return number, threadID, id, nil
}
