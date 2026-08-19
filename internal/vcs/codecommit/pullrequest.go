package codecommit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codecommit"
	"github.com/aws/aws-sdk-go-v2/service/codecommit/types"
	"github.com/aws/smithy-go"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// prCache holds one repository's enumerated pull requests so a listing and
// the finds that follow it share a single pass.
//
// CodeCommit's ListPullRequests returns ids only, so every listing costs a
// GetPullRequest per id, and there is no way at all to search by source
// branch. FindPullRequest therefore has to enumerate and match client side.
// Without this, a repository with N open pull requests would cost N extra
// calls for the listing and another N for every single find.
//
// The cache is NOT per run. A provider is built once by BuildMirrors and
// reused for every tick of the cron schedule, so anything cached here
// outlives the run that filled it. ListPullRequests therefore always
// re-enumerates and replaces the entry, and only FindPullRequest reads it.
// That gives one enumeration per repository per run, and no staleness
// across runs.
type prCache struct {
	mu sync.Mutex
	// key is repository plus status, so the open listing and the closed
	// one, which only a failed find needs, are kept apart.
	byKey map[string][]vcs.PullRequest
}

func cacheKey(repo string, status types.PullRequestStatusEnum) string {
	return repo + "\x00" + string(status)
}

func (c *prCache) get(repo string, status types.PullRequestStatusEnum) ([]vcs.PullRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prs, ok := c.byKey[cacheKey(repo, status)]
	return prs, ok
}

func (c *prCache) put(repo string, status types.PullRequestStatusEnum, prs []vcs.PullRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKey == nil {
		c.byKey = make(map[string][]vcs.PullRequest)
	}
	c.byKey[cacheKey(repo, status)] = prs
}

// invalidate drops everything cached for a repository, in both statuses.
func (c *prCache) invalidate(repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, status := range []types.PullRequestStatusEnum{
		types.PullRequestStatusEnumOpen, types.PullRequestStatusEnumClosed,
	} {
		delete(c.byKey, cacheKey(repo, status))
	}
}

// toPullRequest converts the SDK representation to the neutral model.
//
// CodeCommit reports no draft state and no labels, because it has neither.
func toPullRequest(pr *types.PullRequest, repoName string) vcs.PullRequest {
	out := vcs.PullRequest{
		Title:  aws.ToString(pr.Title),
		Body:   aws.ToString(pr.Description),
		State:  pullRequestState(pr.PullRequestStatus),
		Author: vcs.Actor{Handle: shortARN(aws.ToString(pr.AuthorArn))},
	}
	if n, err := strconv.Atoi(aws.ToString(pr.PullRequestId)); err == nil {
		out.Number = n
	}
	if pr.CreationDate != nil {
		out.CreatedAt = *pr.CreationDate
	}
	if pr.LastActivityDate != nil {
		out.UpdatedAt = *pr.LastActivityDate
	}

	for _, target := range pr.PullRequestTargets {
		if aws.ToString(target.RepositoryName) != repoName {
			continue
		}
		out.HeadBranch = strings.TrimPrefix(aws.ToString(target.SourceReference), "refs/heads/")
		out.BaseBranch = strings.TrimPrefix(aws.ToString(target.DestinationReference), "refs/heads/")
		out.HeadSHA = aws.ToString(target.SourceCommit)
		out.MergeSHA = aws.ToString(target.MergeBase)
		if target.MergeMetadata != nil && target.MergeMetadata.IsMerged {
			out.State = vcs.PRMerged
			out.MergeSHA = aws.ToString(target.MergeMetadata.MergeCommitId)
		}
		break
	}
	return out
}

// pullRequestState maps CodeCommit's two statuses onto the three state
// model. A merged pull request reports CLOSED plus merge metadata, which
// toPullRequest promotes to merged.
func pullRequestState(status types.PullRequestStatusEnum) vcs.PRState {
	if status == types.PullRequestStatusEnumClosed {
		return vcs.PRClosed
	}
	return vcs.PROpen
}

// shortARN reduces an IAM ARN to the trailing name, which is the closest
// thing CodeCommit reports to a handle.
func shortARN(arn string) string {
	if arn == "" {
		return ""
	}
	if i := strings.LastIndex(arn, "/"); i >= 0 && i+1 < len(arn) {
		return arn[i+1:]
	}
	return arn
}

// ListPullRequests returns the open pull requests of one repository.
func (p *Provider) ListPullRequests(ctx context.Context, repoPath string, opts vcs.PRListOptions) ([]vcs.PullRequest, error) {
	for _, s := range opts.States {
		if s != vcs.PROpen {
			return nil, fmt.Errorf("codecommit: listing %s pull requests is not supported", s)
		}
	}
	// Always a fresh enumeration: this is the call the engine makes once per
	// repository per run, and the provider outlives the run. It also drops
	// any closed listing cached during the previous run.
	p.prs.invalidate(repoPath)
	return p.enumerate(ctx, repoPath, types.PullRequestStatusEnumOpen)
}

// FindPullRequest locates a pull request by its source branch.
//
// CodeCommit has no source branch filter, so this matches client side over
// the enumeration, which is shared with ListPullRequests for the run.
func (p *Provider) FindPullRequest(ctx context.Context, repoPath, headBranch string) (vcs.PullRequest, bool, error) {
	// Open first, which is both the common case and already enumerated by
	// the listing this run made. Closed only if that misses: a destination
	// pull request somebody closed still has to be found, or a lost state
	// file would create a second one for the same branch.
	for _, status := range []types.PullRequestStatusEnum{
		types.PullRequestStatusEnumOpen, types.PullRequestStatusEnumClosed,
	} {
		prs, err := p.enumerate(ctx, repoPath, status)
		if err != nil {
			return vcs.PullRequest{}, false, err
		}
		for _, pr := range prs {
			if pr.HeadBranch == headBranch {
				return pr, true, nil
			}
		}
	}
	return vcs.PullRequest{}, false, nil
}

// enumerate lists the open pull request ids and fetches each one, caching
// the result for the rest of the run.
func (p *Provider) enumerate(ctx context.Context, repoPath string, status types.PullRequestStatusEnum) ([]vcs.PullRequest, error) {
	if prs, ok := p.prs.get(repoPath, status); ok {
		return prs, nil
	}

	c, err := p.clientCached(ctx)
	if err != nil {
		return nil, fmt.Errorf("codecommit: construct client: %w", err)
	}

	var ids []string
	var token *string
	seen := make(map[string]bool)

	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf("codecommit: pull request pagination exceeded %d pages, refusing to continue", maxPages)
		}
		out, lerr := c.ListPullRequests(ctx, &codecommit.ListPullRequestsInput{
			RepositoryName:    aws.String(repoPath),
			PullRequestStatus: status,
			NextToken:         token,
		})
		if lerr != nil {
			return nil, fmt.Errorf("codecommit: list pull requests: %w", lerr)
		}
		ids = append(ids, out.PullRequestIds...)

		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		if seen[*out.NextToken] {
			return nil, fmt.Errorf("codecommit: pull request pagination revisited a next token, refusing to loop")
		}
		seen[*out.NextToken] = true
		token = out.NextToken
	}

	prs := make([]vcs.PullRequest, 0, len(ids))
	for _, id := range ids {
		got, gerr := c.GetPullRequest(ctx, &codecommit.GetPullRequestInput{PullRequestId: aws.String(id)})
		if gerr != nil {
			return nil, fmt.Errorf("codecommit: get pull request %s: %w", id, gerr)
		}
		if got.PullRequest == nil {
			continue
		}
		prs = append(prs, toPullRequest(got.PullRequest, repoPath))
	}

	p.prs.put(repoPath, status, prs)
	return prs, nil
}

// CreatePullRequest opens a pull request.
//
// Draft and labels are dropped: CodeCommit has neither concept.
func (p *Provider) CreatePullRequest(ctx context.Context, repoPath string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	c, err := p.clientCached(ctx)
	if err != nil {
		return vcs.PullRequest{}, fmt.Errorf("codecommit: construct client: %w", err)
	}

	out, err := c.CreatePullRequest(ctx, &codecommit.CreatePullRequestInput{
		Title:       aws.String(truncateTitle(spec.Title)),
		Description: aws.String(spec.Body),
		Targets: []types.Target{{
			RepositoryName:       aws.String(repoPath),
			SourceReference:      aws.String("refs/heads/" + spec.HeadBranch),
			DestinationReference: aws.String("refs/heads/" + spec.BaseBranch),
		}},
	})
	if err != nil {
		return vcs.PullRequest{}, fmt.Errorf("codecommit: create pull request: %w", err)
	}

	p.prs.invalidate(repoPath)
	if out.PullRequest == nil {
		return vcs.PullRequest{}, fmt.Errorf("codecommit: create pull request returned no pull request")
	}
	return toPullRequest(out.PullRequest, repoPath), nil
}

// truncateTitle keeps a title inside CodeCommit's documented 150 character
// limit, since a rejected create is worse than a shortened title.
func truncateTitle(title string) string {
	const limit = 150
	if len(title) <= limit {
		return title
	}
	return title[:limit-3] + "..."
}

// UpdatePullRequest rewrites title and description.
//
// CodeCommit has no combined update, so this is two calls. The title goes
// first: if the second call fails, a pull request with a current title and
// a stale body is the less confusing half-applied state.
func (p *Provider) UpdatePullRequest(ctx context.Context, repoPath string, number int, spec vcs.PullRequestSpec) error {
	c, err := p.clientCached(ctx)
	if err != nil {
		return fmt.Errorf("codecommit: construct client: %w", err)
	}
	id := strconv.Itoa(number)

	if _, err := c.UpdatePullRequestTitle(ctx, &codecommit.UpdatePullRequestTitleInput{
		PullRequestId: aws.String(id),
		Title:         aws.String(truncateTitle(spec.Title)),
	}); err != nil {
		return fmt.Errorf("codecommit: update pull request title: %w", err)
	}

	if _, err := c.UpdatePullRequestDescription(ctx, &codecommit.UpdatePullRequestDescriptionInput{
		PullRequestId: aws.String(id),
		Description:   aws.String(spec.Body),
	}); err != nil {
		return fmt.Errorf("codecommit: update pull request description: %w", err)
	}

	p.prs.invalidate(repoPath)
	return nil
}

// ClosePullRequest closes a pull request.
//
// CodeCommit deliberately implements no reopen: it documents that the only
// valid status transition is OPEN to CLOSED, so this provider does not
// implement vcs.PullRequestReopener and a destination pull request closed
// by hand stays closed.
func (p *Provider) ClosePullRequest(ctx context.Context, repoPath string, number int) error {
	c, err := p.clientCached(ctx)
	if err != nil {
		return fmt.Errorf("codecommit: construct client: %w", err)
	}
	if _, err := c.UpdatePullRequestStatus(ctx, &codecommit.UpdatePullRequestStatusInput{
		PullRequestId:     aws.String(strconv.Itoa(number)),
		PullRequestStatus: types.PullRequestStatusEnumClosed,
	}); err != nil {
		return fmt.Errorf("codecommit: close pull request: %w", err)
	}
	p.prs.invalidate(repoPath)
	return nil
}

// ListComments returns the discussion comments on a pull request.
func (p *Provider) ListComments(ctx context.Context, repoPath string, number int) ([]vcs.Comment, error) {
	groups, err := p.listCommentGroups(ctx, number)
	if err != nil {
		return nil, err
	}

	var out []vcs.Comment
	for _, g := range groups {
		if g.Location != nil && aws.ToString(g.Location.FilePath) != "" {
			continue
		}
		for _, c := range g.Comments {
			if c.Deleted {
				continue
			}
			out = append(out, toComment(c))
		}
	}
	return out, nil
}

// ListReviewComments returns the comments anchored to a file and line.
func (p *Provider) ListReviewComments(ctx context.Context, repoPath string, number int) ([]vcs.ReviewComment, error) {
	groups, err := p.listCommentGroups(ctx, number)
	if err != nil {
		return nil, err
	}

	var out []vcs.ReviewComment
	for _, g := range groups {
		if g.Location == nil || aws.ToString(g.Location.FilePath) == "" {
			continue
		}
		for _, c := range g.Comments {
			if c.Deleted {
				continue
			}
			rc := vcs.ReviewComment{
				Comment:   toComment(c),
				Path:      aws.ToString(g.Location.FilePath),
				Side:      "RIGHT",
				CommitSHA: aws.ToString(g.AfterCommitId),
				BaseSHA:   aws.ToString(g.BeforeCommitId),
			}
			if g.Location.FilePosition != nil {
				rc.Line = int(*g.Location.FilePosition)
			}
			if g.Location.RelativeFileVersion == types.RelativeFileVersionEnumBefore {
				rc.Side = "LEFT"
			}
			out = append(out, rc)
		}
	}
	return out, nil
}

// ListReviews returns approval states as verdicts.
//
// CodeCommit has no request changes verdict: the only negative action is
// revoking an approval, which leaves nothing to report.
func (p *Provider) ListReviews(ctx context.Context, repoPath string, number int) ([]vcs.Review, error) {
	c, err := p.clientCached(ctx)
	if err != nil {
		return nil, fmt.Errorf("codecommit: construct client: %w", err)
	}

	// The approval state is per revision, and CodeCommit rejects anything
	// but the current revision id, so the pull request is read first to
	// learn it.
	got, gerr := c.GetPullRequest(ctx, &codecommit.GetPullRequestInput{
		PullRequestId: aws.String(strconv.Itoa(number)),
	})
	if gerr != nil {
		// Reporting "no reviews" here would be a lie with teeth: the caller
		// deletes every mirrored comment it no longer sees at the source,
		// so swallowing this would delete the mirrored verdicts on any
		// throttled or unauthorised call.
		return nil, fmt.Errorf("codecommit: get pull request %d for approvals: %w", number, gerr)
	}
	if got.PullRequest == nil {
		return nil, fmt.Errorf("codecommit: pull request %d not found", number)
	}

	out, err := c.GetPullRequestApprovalStates(ctx, &codecommit.GetPullRequestApprovalStatesInput{
		PullRequestId: aws.String(strconv.Itoa(number)),
		RevisionId:    got.PullRequest.RevisionId,
	})
	if err != nil {
		return nil, fmt.Errorf("codecommit: get approval states for pull request %d: %w", number, err)
	}

	reviews := make([]vcs.Review, 0, len(out.Approvals))
	for _, a := range out.Approvals {
		if a.ApprovalState != types.ApprovalStateApprove {
			continue
		}
		reviews = append(reviews, vcs.Review{
			SourceID: aws.ToString(a.UserArn),
			Author:   vcs.Actor{Handle: shortARN(aws.ToString(a.UserArn))},
			State:    "approved",
		})
	}
	return reviews, nil
}

// toComment converts an SDK comment to the neutral model.
func toComment(c types.Comment) vcs.Comment {
	out := vcs.Comment{
		SourceID: aws.ToString(c.CommentId),
		Author:   vcs.Actor{Handle: shortARN(aws.ToString(c.AuthorArn))},
		Body:     aws.ToString(c.Content),
	}
	if c.CreationDate != nil {
		out.CreatedAt = *c.CreationDate
	}
	if c.LastModifiedDate != nil {
		out.UpdatedAt = *c.LastModifiedDate
	}
	return out
}

// listCommentGroups walks the paginated comment listing for one pull
// request.
func (p *Provider) listCommentGroups(ctx context.Context, number int) ([]types.CommentsForPullRequest, error) {
	c, err := p.clientCached(ctx)
	if err != nil {
		return nil, fmt.Errorf("codecommit: construct client: %w", err)
	}

	var out []types.CommentsForPullRequest
	var token *string
	seen := make(map[string]bool)

	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("codecommit: comment pagination exceeded %d pages, refusing to continue", maxPages)
		}
		got, gerr := c.GetCommentsForPullRequest(ctx, &codecommit.GetCommentsForPullRequestInput{
			PullRequestId: aws.String(strconv.Itoa(number)),
			NextToken:     token,
		})
		if gerr != nil {
			return out, fmt.Errorf("codecommit: get comments: %w", gerr)
		}
		out = append(out, got.CommentsForPullRequestData...)

		if got.NextToken == nil || *got.NextToken == "" {
			return out, nil
		}
		if seen[*got.NextToken] {
			return out, fmt.Errorf("codecommit: comment pagination revisited a next token, refusing to loop")
		}
		seen[*got.NextToken] = true
		token = got.NextToken
	}
}

// CreateComment posts a discussion comment.
//
// CodeCommit requires the before and after commit ids on every comment, so
// the pull request is read first to learn them.
func (p *Provider) CreateComment(ctx context.Context, repoPath string, number int, body string) (string, error) {
	return p.postComment(ctx, repoPath, number, body, nil)
}

// CreateReviewComment posts a comment anchored to a file and line.
func (p *Provider) CreateReviewComment(ctx context.Context, repoPath string, number int, rc vcs.ReviewComment) (string, error) {
	if rc.Path == "" {
		return "", fmt.Errorf("%w: codecommit needs a file path", vcs.ErrAnchorRejected)
	}

	location := &types.Location{
		FilePath:            aws.String(rc.Path),
		RelativeFileVersion: types.RelativeFileVersionEnumAfter,
	}
	if rc.Line > 0 {
		location.FilePosition = aws.Int64(int64(rc.Line))
	}
	if rc.Side == "LEFT" {
		location.RelativeFileVersion = types.RelativeFileVersionEnumBefore
	}

	return p.postComment(ctx, repoPath, number, rc.Body, location)
}

// postComment posts a comment, with or without a location.
func (p *Provider) postComment(ctx context.Context, repoPath string, number int, body string, location *types.Location) (string, error) {
	c, err := p.clientCached(ctx)
	if err != nil {
		return "", fmt.Errorf("codecommit: construct client: %w", err)
	}

	before, after, err := p.commitRange(ctx, number, repoPath)
	if err != nil {
		return "", err
	}

	out, err := c.PostCommentForPullRequest(ctx, &codecommit.PostCommentForPullRequestInput{
		PullRequestId:  aws.String(strconv.Itoa(number)),
		RepositoryName: aws.String(repoPath),
		BeforeCommitId: aws.String(before),
		AfterCommitId:  aws.String(after),
		Content:        aws.String(body),
		Location:       location,
	})
	if err != nil {
		if location != nil && isLocationError(err) {
			// A location CodeCommit will not place is a rejected anchor,
			// which the caller downgrades rather than failing on.
			return "", fmt.Errorf("%w: %s", vcs.ErrAnchorRejected, err)
		}
		return "", fmt.Errorf("codecommit: post comment: %w", err)
	}
	if out.Comment == nil {
		return "", fmt.Errorf("codecommit: post comment returned no comment")
	}
	return aws.ToString(out.Comment.CommentId), nil
}

// locationErrors are the CodeCommit errors that mean "this anchor cannot be
// placed" rather than "this call failed".
//
// The distinction matters: a downgrade is permanent, since the discussion
// comment it falls back to is recorded in state and never retried as an
// anchored comment. Treating throttling or an expired credential as a
// rejected anchor would silently and permanently lose the anchor over a
// transient failure.
var locationErrors = []string{
	"InvalidFileLocationException",
	"InvalidFilePositionException",
	"InvalidRelativeFileVersionEnumException",
	"PathDoesNotExistException",
	"PathRequiredException",
	"BeforeCommitIdAndAfterCommitIdAreSameException",
	"CommitDoesNotExistException",
}

// isLocationError reports whether err is about the anchor rather than the
// request.
func isLocationError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		for _, name := range locationErrors {
			if apiErr.ErrorCode() == name {
				return true
			}
		}
		return false
	}
	// Not a modelled API error. Fall back to the text, which is what a
	// wrapped or faked error carries.
	msg := err.Error()
	for _, name := range locationErrors {
		if strings.Contains(msg, name) {
			return true
		}
	}
	return false
}

// commitRange reports the before and after commit ids a comment must carry.
func (p *Provider) commitRange(ctx context.Context, number int, repoPath string) (before, after string, err error) {
	c, cerr := p.clientCached(ctx)
	if cerr != nil {
		return "", "", fmt.Errorf("codecommit: construct client: %w", cerr)
	}

	got, gerr := c.GetPullRequest(ctx, &codecommit.GetPullRequestInput{
		PullRequestId: aws.String(strconv.Itoa(number)),
	})
	if gerr != nil {
		return "", "", fmt.Errorf("codecommit: get pull request %d: %w", number, gerr)
	}
	if got.PullRequest == nil {
		return "", "", fmt.Errorf("codecommit: pull request %d not found", number)
	}

	for _, target := range got.PullRequest.PullRequestTargets {
		if aws.ToString(target.RepositoryName) != repoPath {
			continue
		}
		return aws.ToString(target.DestinationCommit), aws.ToString(target.SourceCommit), nil
	}
	return "", "", fmt.Errorf("codecommit: pull request %d has no target in repository %q", number, repoPath)
}

// UpdateComment rewrites a comment SyncerD created earlier.
func (p *Provider) UpdateComment(ctx context.Context, repoPath, commentID, body string) error {
	c, err := p.clientCached(ctx)
	if err != nil {
		return fmt.Errorf("codecommit: construct client: %w", err)
	}
	_, err = c.UpdateComment(ctx, &codecommit.UpdateCommentInput{
		CommentId: aws.String(commentID),
		Content:   aws.String(body),
	})
	return err
}

// DeleteComment removes the content of a comment SyncerD created earlier.
//
// CodeCommit has no hard delete: DeleteCommentContent blanks the content
// and marks the comment deleted, leaving the comment itself in place. A
// source deletion therefore mirrors as an empty comment rather than
// disappearing.
func (p *Provider) DeleteComment(ctx context.Context, repoPath, commentID string) error {
	c, err := p.clientCached(ctx)
	if err != nil {
		return fmt.Errorf("codecommit: construct client: %w", err)
	}
	_, err = c.DeleteCommentContent(ctx, &codecommit.DeleteCommentContentInput{
		CommentId: aws.String(commentID),
	})
	return err
}
