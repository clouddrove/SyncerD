package codecommit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codecommit"
	"github.com/aws/aws-sdk-go-v2/service/codecommit/types"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func TestProviderImplementsThePullRequestInterfaces(t *testing.T) {
	var _ vcs.PullRequestLister = (*Provider)(nil)
	var _ vcs.PullRequestWriter = (*Provider)(nil)
	var _ vcs.PullRequestConversation = (*Provider)(nil)
}

func TestProviderDeliberatelyCannotReopen(t *testing.T) {
	// CodeCommit documents that the only valid status transition is OPEN
	// to CLOSED, so the capability must be absent rather than failing at
	// run time.
	if _, ok := any((*Provider)(nil)).(vcs.PullRequestReopener); ok {
		t.Fatal("CodeCommit cannot reopen a closed pull request and must not claim it can")
	}
}

// prFake wires a fake that serves two open pull requests.
func prFake() *fakeAPI {
	f := &fakeAPI{}
	f.listPRFn = func(context.Context, *codecommit.ListPullRequestsInput) (*codecommit.ListPullRequestsOutput, error) {
		return &codecommit.ListPullRequestsOutput{PullRequestIds: []string{"7", "8"}}, nil
	}
	f.getPRFn = func(_ context.Context, in *codecommit.GetPullRequestInput) (*codecommit.GetPullRequestOutput, error) {
		id := aws.ToString(in.PullRequestId)
		source := "refs/heads/feature"
		if id == "8" {
			source = "refs/heads/syncerd/pr/7"
		}
		return &codecommit.GetPullRequestOutput{PullRequest: &types.PullRequest{
			PullRequestId:     aws.String(id),
			Title:             aws.String("Add login"),
			Description:       aws.String("body"),
			PullRequestStatus: types.PullRequestStatusEnumOpen,
			AuthorArn:         aws.String("arn:aws:iam::1:user/alice"),
			RevisionId:        aws.String("rev1"),
			PullRequestTargets: []types.PullRequestTarget{{
				RepositoryName:       aws.String("widget"),
				SourceReference:      aws.String(source),
				DestinationReference: aws.String("refs/heads/main"),
				SourceCommit:         aws.String("aaa"),
				DestinationCommit:    aws.String("bbb"),
			}},
		}}, nil
	}
	return f
}

func TestListPullRequestsFetchesEachIDAndDecodes(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	prs, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d pull requests, want 2", len(prs))
	}
	// ListPullRequests returns ids only, so each one costs a GetPullRequest.
	if f.listPRCalls != 1 || f.getPRCalls != 2 {
		t.Errorf("calls: list=%d get=%d, want 1 and 2", f.listPRCalls, f.getPRCalls)
	}

	pr := prs[0]
	if pr.Number != 7 || pr.HeadBranch != "feature" || pr.BaseBranch != "main" || pr.HeadSHA != "aaa" {
		t.Errorf("decoded wrong: %+v", pr)
	}
	if pr.Author.Handle != "alice" {
		t.Errorf("Author = %+v, want the ARN reduced to a name", pr.Author)
	}
	if pr.Draft {
		t.Error("CodeCommit has no draft concept")
	}
	if len(pr.Labels) != 0 {
		t.Error("CodeCommit has no pull request labels")
	}
}

func TestFindPullRequestMatchesClientSideAndReusesTheEnumeration(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	if _, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	getsAfterList := f.getPRCalls

	got, ok, err := p.FindPullRequest(context.Background(), "widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || got.Number != 8 {
		t.Fatalf("find = %+v, %v", got, ok)
	}
	// CodeCommit has no branch filter, so this is a client side match over
	// the enumeration. Doing it again must not re-enumerate.
	if f.getPRCalls != getsAfterList {
		t.Errorf("find re-enumerated: %d gets after list, %d after find", getsAfterList, f.getPRCalls)
	}
}

func TestFindPullRequestReportsAbsence(t *testing.T) {
	p := newProvider(t, prFake())
	if _, ok, err := p.FindPullRequest(context.Background(), "widget", "nope"); ok || err != nil {
		t.Fatalf("want absence with no error, got ok=%v err=%v", ok, err)
	}
}

func TestMergedPullRequestIsReportedAsMerged(t *testing.T) {
	f := &fakeAPI{}
	f.listPRFn = func(context.Context, *codecommit.ListPullRequestsInput) (*codecommit.ListPullRequestsOutput, error) {
		return &codecommit.ListPullRequestsOutput{PullRequestIds: []string{"9"}}, nil
	}
	f.getPRFn = func(context.Context, *codecommit.GetPullRequestInput) (*codecommit.GetPullRequestOutput, error) {
		return &codecommit.GetPullRequestOutput{PullRequest: &types.PullRequest{
			PullRequestId:     aws.String("9"),
			PullRequestStatus: types.PullRequestStatusEnumClosed,
			PullRequestTargets: []types.PullRequestTarget{{
				RepositoryName:  aws.String("widget"),
				SourceReference: aws.String("refs/heads/done"),
				MergeMetadata:   &types.MergeMetadata{IsMerged: true, MergeCommitId: aws.String("deadbeef")},
			}},
		}}, nil
	}

	p := newProvider(t, f)
	prs, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if prs[0].State != vcs.PRMerged || prs[0].MergeSHA != "deadbeef" {
		t.Errorf("merge metadata lost: %+v", prs[0])
	}
}

func TestCreatePullRequestSendsFullRefs(t *testing.T) {
	var in *codecommit.CreatePullRequestInput
	f := prFake()
	f.createPRFn = func(_ context.Context, got *codecommit.CreatePullRequestInput) (*codecommit.CreatePullRequestOutput, error) {
		in = got
		return &codecommit.CreatePullRequestOutput{PullRequest: &types.PullRequest{
			PullRequestId:     aws.String("12"),
			PullRequestStatus: types.PullRequestStatusEnumOpen,
		}}, nil
	}

	p := newProvider(t, f)
	got, err := p.CreatePullRequest(context.Background(), "widget", vcs.PullRequestSpec{
		Title: "Add login", Body: "body", HeadBranch: "syncerd/pr/7", BaseBranch: "main",
		Draft: true, Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Number != 12 {
		t.Errorf("Number = %d", got.Number)
	}
	target := in.Targets[0]
	if aws.ToString(target.SourceReference) != "refs/heads/syncerd/pr/7" {
		t.Errorf("source reference = %q", aws.ToString(target.SourceReference))
	}
	if aws.ToString(target.DestinationReference) != "refs/heads/main" {
		t.Errorf("destination reference = %q", aws.ToString(target.DestinationReference))
	}
}

func TestTitleIsTruncatedToTheDocumentedLimit(t *testing.T) {
	long := strings.Repeat("x", 200)
	if got := truncateTitle(long); len(got) > 150 {
		t.Fatalf("title is %d characters, over the 150 limit", len(got))
	}
	if got := truncateTitle("fine"); got != "fine" {
		t.Errorf("a short title must be untouched, got %q", got)
	}
}

func TestUpdatePullRequestIsTwoCalls(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	err := p.UpdatePullRequest(context.Background(), "widget", 7, vcs.PullRequestSpec{
		Title: "Add login v2", Body: "new body",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// CodeCommit has no combined update.
	if len(f.titleUpdates) != 1 || f.titleUpdates[0] != "Add login v2" {
		t.Errorf("title updates = %v", f.titleUpdates)
	}
	if len(f.descUpdates) != 1 || f.descUpdates[0] != "new body" {
		t.Errorf("description updates = %v", f.descUpdates)
	}
}

func TestClosePullRequestSetsClosedAndNeverMerges(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	if err := p.ClosePullRequest(context.Background(), "widget", 7); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(f.statusUpdates) != 1 || f.statusUpdates[0] != string(types.PullRequestStatusEnumClosed) {
		t.Errorf("status updates = %v, want one CLOSED", f.statusUpdates)
	}
}

func TestCommentsSplitByLocation(t *testing.T) {
	f := prFake()
	f.commentsFn = func(context.Context, *codecommit.GetCommentsForPullRequestInput) (*codecommit.GetCommentsForPullRequestOutput, error) {
		return &codecommit.GetCommentsForPullRequestOutput{
			CommentsForPullRequestData: []types.CommentsForPullRequest{
				{
					Comments: []types.Comment{{
						CommentId: aws.String("c1"), Content: aws.String("a discussion comment"),
						AuthorArn: aws.String("arn:aws:iam::1:user/bob"),
					}},
				},
				{
					BeforeCommitId: aws.String("bbb"),
					AfterCommitId:  aws.String("aaa"),
					Location: &types.Location{
						FilePath:            aws.String("internal/app.go"),
						FilePosition:        aws.Int64(42),
						RelativeFileVersion: types.RelativeFileVersionEnumAfter,
					},
					Comments: []types.Comment{{
						CommentId: aws.String("c2"), Content: aws.String("nit"),
						AuthorArn: aws.String("arn:aws:iam::1:user/rev"),
					}},
				},
				{
					Comments: []types.Comment{{
						CommentId: aws.String("c3"), Content: aws.String(""), Deleted: true,
					}},
				},
			},
		}, nil
	}

	p := newProvider(t, f)

	discussion, err := p.ListComments(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(discussion) != 1 || discussion[0].Body != "a discussion comment" {
		t.Fatalf("discussion = %+v", discussion)
	}

	inline, err := p.ListReviewComments(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list review comments: %v", err)
	}
	if len(inline) != 1 {
		t.Fatalf("inline = %+v", inline)
	}
	rc := inline[0]
	if rc.Path != "internal/app.go" || rc.Line != 42 {
		t.Errorf("anchor wrong: %+v", rc)
	}
	// CodeCommit anchors against an explicit commit pair, which the model
	// carries as the head and base SHAs.
	if rc.CommitSHA != "aaa" || rc.BaseSHA != "bbb" {
		t.Errorf("commit pair lost: %+v", rc)
	}
}

func TestPostCommentCarriesTheCommitPair(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	id, err := p.CreateComment(context.Background(), "widget", 7, "hello")
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if id != "c1" {
		t.Errorf("id = %q", id)
	}
	if len(f.posted) != 1 {
		t.Fatalf("posted = %+v", f.posted)
	}
	// CodeCommit requires both commit ids on every comment, so they are
	// read from the pull request first.
	if aws.ToString(f.posted[0].BeforeCommitId) != "bbb" || aws.ToString(f.posted[0].AfterCommitId) != "aaa" {
		t.Errorf("commit pair wrong: before=%q after=%q",
			aws.ToString(f.posted[0].BeforeCommitId), aws.ToString(f.posted[0].AfterCommitId))
	}
	if f.posted[0].Location != nil {
		t.Error("a discussion comment must carry no location")
	}
}

func TestCreateReviewCommentSendsALocation(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	_, err := p.CreateReviewComment(context.Background(), "widget", 7, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "internal/app.go", Line: 42, Side: "RIGHT",
	})
	if err != nil {
		t.Fatalf("create review comment: %v", err)
	}
	loc := f.posted[0].Location
	if loc == nil || aws.ToString(loc.FilePath) != "internal/app.go" {
		t.Fatalf("location = %+v", loc)
	}
	if loc.FilePosition == nil || *loc.FilePosition != 42 {
		t.Errorf("file position = %+v", loc.FilePosition)
	}
	if loc.RelativeFileVersion != types.RelativeFileVersionEnumAfter {
		t.Errorf("relative version = %q", loc.RelativeFileVersion)
	}
}

func TestRejectedLocationDowngradesRatherThanFailing(t *testing.T) {
	f := prFake()
	f.postFn = func(context.Context, *codecommit.PostCommentForPullRequestInput) (*codecommit.PostCommentForPullRequestOutput, error) {
		return nil, errors.New("InvalidFileLocationException")
	}

	p := newProvider(t, f)
	_, err := p.CreateReviewComment(context.Background(), "widget", 7, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "internal/app.go", Line: 42,
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("a refused location must downgrade, got %v", err)
	}
}

func TestCreateReviewCommentWithoutAPathIsARejectedAnchor(t *testing.T) {
	p := newProvider(t, prFake())
	_, err := p.CreateReviewComment(context.Background(), "widget", 7, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Line: 42,
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("want ErrAnchorRejected, got %v", err)
	}
}

func TestDeleteCommentIsASoftDelete(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	if err := p.DeleteComment(context.Background(), "widget", "c1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// CodeCommit has no hard delete: this blanks the content and leaves a
	// tombstone, which is the most the provider can do.
	if len(f.commentDeletes) != 1 || f.commentDeletes[0] != "c1" {
		t.Errorf("deletes = %v", f.commentDeletes)
	}
}

func TestListReviewsReadsApprovalsAtTheCurrentRevision(t *testing.T) {
	var gotRevision string
	f := prFake()
	f.approvalFn = func(_ context.Context, in *codecommit.GetPullRequestApprovalStatesInput) (*codecommit.GetPullRequestApprovalStatesOutput, error) {
		gotRevision = aws.ToString(in.RevisionId)
		return &codecommit.GetPullRequestApprovalStatesOutput{Approvals: []types.Approval{
			{UserArn: aws.String("arn:aws:iam::1:user/maintainer"), ApprovalState: types.ApprovalStateApprove},
			{UserArn: aws.String("arn:aws:iam::1:user/other"), ApprovalState: types.ApprovalStateRevoke},
		}}, nil
	}

	p := newProvider(t, f)
	got, err := p.ListReviews(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	// CodeCommit rejects anything but the current revision id.
	if gotRevision != "rev1" {
		t.Errorf("revision = %q, want the current one", gotRevision)
	}
	if len(got) != 1 || got[0].State != "approved" || got[0].Author.Handle != "maintainer" {
		t.Errorf("a revoked approval is not a verdict: %+v", got)
	}
}

func TestListReviewsSurfacesErrorsRatherThanReportingNoReviews(t *testing.T) {
	f := prFake()
	f.approvalFn = func(context.Context, *codecommit.GetPullRequestApprovalStatesInput) (*codecommit.GetPullRequestApprovalStatesOutput, error) {
		return nil, errors.New("ThrottlingException")
	}

	p := newProvider(t, f)
	// Answering "no reviews" on a failed call would be a lie with teeth:
	// the caller deletes mirrored comments it no longer sees at the source,
	// so a throttled call would delete every mirrored verdict.
	if _, err := p.ListReviews(context.Background(), "widget", 7); err == nil {
		t.Fatal("a failed approval lookup must be an error, not an empty result")
	}
}

func TestListReviewsIsEmptyWhenNobodyHasApproved(t *testing.T) {
	f := prFake()
	f.approvalFn = func(context.Context, *codecommit.GetPullRequestApprovalStatesInput) (*codecommit.GetPullRequestApprovalStatesOutput, error) {
		return &codecommit.GetPullRequestApprovalStatesOutput{}, nil
	}

	p := newProvider(t, f)
	got, err := p.ListReviews(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %+v, want none", got)
	}
}

func TestFindPullRequestLooksAtClosedPullRequestsToo(t *testing.T) {
	f := &fakeAPI{}
	f.listPRFn = func(_ context.Context, in *codecommit.ListPullRequestsInput) (*codecommit.ListPullRequestsOutput, error) {
		if in.PullRequestStatus == types.PullRequestStatusEnumClosed {
			return &codecommit.ListPullRequestsOutput{PullRequestIds: []string{"9"}}, nil
		}
		return &codecommit.ListPullRequestsOutput{}, nil
	}
	f.getPRFn = func(context.Context, *codecommit.GetPullRequestInput) (*codecommit.GetPullRequestOutput, error) {
		return &codecommit.GetPullRequestOutput{PullRequest: &types.PullRequest{
			PullRequestId:     aws.String("9"),
			PullRequestStatus: types.PullRequestStatusEnumClosed,
			PullRequestTargets: []types.PullRequestTarget{{
				RepositoryName:  aws.String("widget"),
				SourceReference: aws.String("refs/heads/syncerd/pr/7"),
			}},
		}}, nil
	}

	p := newProvider(t, f)
	// A destination pull request somebody closed must still be findable, or
	// a lost state file creates a second one for the same branch.
	got, ok, err := p.FindPullRequest(context.Background(), "widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || got.Number != 9 || got.State != vcs.PRClosed {
		t.Fatalf("find = %+v, %v", got, ok)
	}
}

func TestListingRefreshesRatherThanServingAStaleRun(t *testing.T) {
	f := prFake()
	p := newProvider(t, f)

	if _, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := f.listPRCalls

	// A provider is built once and reused for every cron tick, so a second
	// run must not be served the first run's snapshot.
	if _, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if f.listPRCalls == first {
		t.Fatal("a second listing must re-enumerate; the cache outlives the run")
	}
}

func TestATransientErrorDoesNotPermanentlyDowngradeAnAnchor(t *testing.T) {
	f := prFake()
	f.postFn = func(context.Context, *codecommit.PostCommentForPullRequestInput) (*codecommit.PostCommentForPullRequestOutput, error) {
		return nil, errors.New("ThrottlingException: Rate exceeded")
	}

	p := newProvider(t, f)
	_, err := p.CreateReviewComment(context.Background(), "widget", 7, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "internal/app.go", Line: 42,
	})
	if errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatal("throttling is not a rejected anchor: downgrading is permanent, so it must not happen on a transient failure")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}
