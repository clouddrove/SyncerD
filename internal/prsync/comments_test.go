package prsync

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func convOpts(t *testing.T, dest *fakeDest, source, destination *fakeConv) Options {
	o := baseOpts(t, dest)
	o.SourceConv = source
	o.DestConv = destination
	o.Comments = true
	o.Reviews = true
	return o
}

func TestDiscussionCommentsMirrorOnceAndUpdateInPlace(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)

	src.comments = []vcs.Comment{{
		SourceID: "991", Author: vcs.Actor{Handle: "bob"}, Body: "first thought",
		CreatedAt: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
	}}

	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))
	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if res.CommentsCreated != 1 {
		t.Fatalf("CommentsCreated = %d, want 1", res.CommentsCreated)
	}
	if len(dst.posted) != 1 || !strings.Contains(dst.posted[0], "first thought") {
		t.Fatalf("comment not mirrored: %+v", dst.posted)
	}

	// The comment is edited at the source; the mirror rewrites in place.
	src.comments[0].Body = "second thought"
	pr.UpdatedAt = pr.UpdatedAt.Add(time.Hour)

	res, err = Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.CommentsCreated != 0 {
		t.Errorf("an edited comment must update, not repost: %+v", res)
	}
	if len(dst.posted) != 1 {
		t.Errorf("a second copy was posted: %+v", dst.posted)
	}
	if len(dst.updates) != 1 {
		t.Fatalf("the mirrored comment was not updated: %+v", dst.updates)
	}
	for _, body := range dst.updates {
		if !strings.Contains(body, "second thought") {
			t.Errorf("update body = %q", body)
		}
	}
}

func TestDeletedSourceCommentDeletesOnlyTheMirroredCopy(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)

	src.comments = []vcs.Comment{{SourceID: "991", Body: "temporary", CreatedAt: time.Now()}}
	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	// The source comment is deleted. A comment written by a person at the
	// destination is not in CommentIDs at all, so it can never be touched
	// by this path.
	src.comments = nil
	pr.UpdatedAt = pr.UpdatedAt.Add(time.Hour)
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("second: %v", err)
	}

	if len(dst.deletes) != 1 {
		t.Fatalf("the mirrored copy should be deleted, deletes = %+v", dst.deletes)
	}
	rec, _ := opts.State.GetPR("gh-to-gh", "acme/widget", 7)
	if _, still := rec.CommentIDs["991"]; still {
		t.Error("state should forget a comment it deleted")
	}
}

func TestReviewCommentKeepsItsAnchor(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)

	src.reviewComments = []vcs.ReviewComment{{
		Comment:   vcs.Comment{SourceID: "5", Author: vcs.Actor{Handle: "rev"}, Body: "nit", CreatedAt: time.Now()},
		Path:      "internal/app.go",
		Line:      42,
		Side:      "RIGHT",
		CommitSHA: "abc123",
	}}

	if _, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(dst.anchored) != 1 {
		t.Fatalf("the comment should be anchored to the diff: %+v", dst.anchored)
	}
	got := dst.anchored[0]
	if got.Path != "internal/app.go" || got.Line != 42 || got.CommitSHA != "abc123" {
		t.Errorf("anchor lost: %+v", got)
	}
	if !strings.Contains(got.Body, "nit") {
		t.Errorf("body lost: %q", got.Body)
	}
}

func TestRejectedAnchorDowngradesRatherThanDropping(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	dst.anchorErr = fmt.Errorf("%w: internal/app.go:42", vcs.ErrAnchorRejected)
	opts := convOpts(t, dest, src, dst)

	src.reviewComments = []vcs.ReviewComment{{
		Comment: vcs.Comment{SourceID: "5", Body: "nit", CreatedAt: time.Now()},
		Path:    "internal/app.go", Line: 42, CommitSHA: "abc123",
	}}

	res, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts)
	if err != nil {
		t.Fatalf("a rejected anchor must not fail the pull request: %v", err)
	}
	if res.Downgraded != 1 {
		t.Errorf("Downgraded = %d, want 1", res.Downgraded)
	}
	if len(dst.posted) != 1 {
		t.Fatalf("the remark should survive as a discussion comment: %+v", dst.posted)
	}
	if !strings.Contains(dst.posted[0], "internal/app.go:42") {
		t.Errorf("the downgraded comment must name the line it could not anchor to: %q", dst.posted[0])
	}
	if !strings.Contains(dst.posted[0], "nit") {
		t.Errorf("the remark itself must survive: %q", dst.posted[0])
	}
}

func TestReviewVerdictIsMirroredAsTextNotAsAnApproval(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)

	src.reviews = []vcs.Review{{
		SourceID: "55", Author: vcs.Actor{Handle: "maintainer"},
		State: "approved", Body: "ship it", SubmittedAt: time.Now(),
	}}

	res, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.CommentsCreated != 1 {
		t.Fatalf("CommentsCreated = %d, want 1", res.CommentsCreated)
	}
	if len(dst.anchored) != 0 {
		t.Error("a verdict is not an inline comment")
	}
	body := dst.posted[0]
	if !strings.Contains(body, "approved") || !strings.Contains(body, "not an approval here") {
		t.Errorf("a mirrored verdict must name the verdict and disclaim it: %q", body)
	}
}

func TestConversationIsSkippedWhenDisabled(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)
	opts.Comments = false
	opts.Reviews = false

	src.comments = []vcs.Comment{{SourceID: "991", Body: "hello", CreatedAt: time.Now()}}
	src.reviews = []vcs.Review{{SourceID: "55", State: "approved", SubmittedAt: time.Now()}}

	if _, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(dst.posted) != 0 {
		t.Errorf("nothing should be mirrored when both switches are off: %+v", dst.posted)
	}
}

func TestConversationDryRunReportsWithoutWriting(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)

	// The pull request has to exist at the destination before its
	// conversation has anywhere to go, so this exercises the dry run on the
	// second visit rather than the first.
	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	opts.DryRun = true
	src.comments = []vcs.Comment{{SourceID: "991", Body: "hello", CreatedAt: time.Now()}}
	pr.UpdatedAt = pr.UpdatedAt.Add(time.Hour)

	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(dst.posted) != 0 || len(dst.updates) != 0 || len(dst.deletes) != 0 {
		t.Error("a dry run must not write any comment")
	}
	if res.CommentsCreated != 1 {
		t.Errorf("a dry run must still report what it would post, got %d", res.CommentsCreated)
	}
}

func TestDryRunOnAPullRequestThatDoesNotExistYetReportsOnlyTheCreation(t *testing.T) {
	dest := newFakeDest(t)
	src := newFakeConv()
	dst := newFakeConv()
	opts := convOpts(t, dest, src, dst)
	opts.DryRun = true

	src.comments = []vcs.Comment{{SourceID: "991", Body: "hello", CreatedAt: time.Now()}}

	res, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Created != 1 {
		t.Errorf("Created = %d, want 1", res.Created)
	}
	// There is no destination pull request number to hang a conversation
	// off yet, so the comment count is genuinely unknown rather than zero
	// by accident. Reporting the creation alone is the honest answer.
	if res.CommentsCreated != 0 {
		t.Errorf("CommentsCreated = %d, want 0 until the pull request exists", res.CommentsCreated)
	}
	if len(dst.posted) != 0 {
		t.Error("a dry run must not write any comment")
	}
}
