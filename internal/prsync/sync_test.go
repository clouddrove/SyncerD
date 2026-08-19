package prsync

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
)

// fakeDest records everything written to a destination. It fails the test
// if a merge is ever attempted, which is the rule the whole design rests on.
type fakeDest struct {
	t *testing.T

	existing    map[string]vcs.PullRequest // head branch -> pull request
	nextNumber  int
	created     []vcs.PullRequestSpec
	updated     []vcs.PullRequestSpec
	stateCalls  []vcs.PRState
	findErr     error
	createErr   error
	findCalls   int
	stateByNum  map[int]vcs.PRState
	mergeCalled bool
}

func newFakeDest(t *testing.T) *fakeDest {
	return &fakeDest{
		t:          t,
		existing:   map[string]vcs.PullRequest{},
		nextNumber: 100,
		stateByNum: map[int]vcs.PRState{},
	}
}

func (f *fakeDest) FindPullRequest(_ context.Context, _, headBranch string) (vcs.PullRequest, bool, error) {
	f.findCalls++
	if f.findErr != nil {
		return vcs.PullRequest{}, false, f.findErr
	}
	pr, ok := f.existing[headBranch]
	return pr, ok, nil
}

func (f *fakeDest) CreatePullRequest(_ context.Context, _ string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	if f.createErr != nil {
		return vcs.PullRequest{}, f.createErr
	}
	f.created = append(f.created, spec)
	f.nextNumber++
	pr := vcs.PullRequest{Number: f.nextNumber, State: vcs.PROpen, HeadBranch: spec.HeadBranch}
	f.existing[spec.HeadBranch] = pr
	f.stateByNum[pr.Number] = vcs.PROpen
	return pr, nil
}

func (f *fakeDest) UpdatePullRequest(_ context.Context, _ string, _ int, spec vcs.PullRequestSpec) error {
	f.updated = append(f.updated, spec)
	return nil
}

func (f *fakeDest) ClosePullRequest(_ context.Context, _ string, number int) error {
	f.stateCalls = append(f.stateCalls, vcs.PRClosed)
	f.stateByNum[number] = vcs.PRClosed
	return nil
}

func (f *fakeDest) ReopenPullRequest(_ context.Context, _ string, number int) error {
	f.stateCalls = append(f.stateCalls, vcs.PROpen)
	f.stateByNum[number] = vcs.PROpen
	return nil
}

// noReopenDest stands in for Bitbucket and CodeCommit, which have no reopen
// endpoint at all.
//
// It forwards each writer method by hand rather than embedding *fakeDest,
// because embedding would promote ReopenPullRequest and the type would
// satisfy vcs.PullRequestReopener after all, which is exactly what this
// fake exists to not do.
type noReopenDest struct{ inner *fakeDest }

func (n noReopenDest) FindPullRequest(ctx context.Context, repo, head string) (vcs.PullRequest, bool, error) {
	return n.inner.FindPullRequest(ctx, repo, head)
}
func (n noReopenDest) CreatePullRequest(ctx context.Context, repo string, spec vcs.PullRequestSpec) (vcs.PullRequest, error) {
	return n.inner.CreatePullRequest(ctx, repo, spec)
}
func (n noReopenDest) UpdatePullRequest(ctx context.Context, repo string, number int, spec vcs.PullRequestSpec) error {
	return n.inner.UpdatePullRequest(ctx, repo, number, spec)
}
func (n noReopenDest) ClosePullRequest(ctx context.Context, repo string, number int) error {
	return n.inner.ClosePullRequest(ctx, repo, number)
}

// fakeConv is a conversation endpoint on both sides.
type fakeConv struct {
	comments       []vcs.Comment
	reviewComments []vcs.ReviewComment
	reviews        []vcs.Review

	posted    []string
	updates   map[string]string
	deletes   []string
	anchored  []vcs.ReviewComment
	anchorErr error
	nextID    int
}

func newFakeConv() *fakeConv {
	return &fakeConv{updates: map[string]string{}, nextID: 500}
}

func (f *fakeConv) ListComments(context.Context, string, int) ([]vcs.Comment, error) {
	return f.comments, nil
}
func (f *fakeConv) ListReviewComments(context.Context, string, int) ([]vcs.ReviewComment, error) {
	return f.reviewComments, nil
}
func (f *fakeConv) ListReviews(context.Context, string, int) ([]vcs.Review, error) {
	return f.reviews, nil
}
func (f *fakeConv) CreateComment(_ context.Context, _ string, _ int, body string) (string, error) {
	f.posted = append(f.posted, body)
	f.nextID++
	return strconv.Itoa(f.nextID), nil
}
func (f *fakeConv) UpdateComment(_ context.Context, _, commentID, body string) error {
	f.updates[commentID] = body
	return nil
}
func (f *fakeConv) DeleteComment(_ context.Context, _, commentID string) error {
	f.deletes = append(f.deletes, commentID)
	return nil
}
func (f *fakeConv) CreateReviewComment(_ context.Context, _ string, _ int, rc vcs.ReviewComment) (string, error) {
	if f.anchorErr != nil {
		return "", f.anchorErr
	}
	f.anchored = append(f.anchored, rc)
	f.nextID++
	return strconv.Itoa(f.nextID), nil
}

func baseOpts(t *testing.T, dest *fakeDest) Options {
	return Options{
		Mirror:       "gh-to-gh",
		SourceRepo:   "acme/widget",
		DestRepo:     "acme/widget-mirror",
		BranchPrefix: "syncerd/pr",
		Dest:         dest,
		State:        state.NewGit(),
		Labels:       true,
	}
}

func openPR(n int, updated time.Time) vcs.PullRequest {
	return vcs.PullRequest{
		Number: n, Title: "Add login", Body: "body", State: vcs.PROpen,
		HeadBranch: "feature", BaseBranch: "main", UpdatedAt: updated,
		Author: vcs.Actor{Handle: "alice"}, Labels: []string{"bug"},
	}
}

func TestCreatesAWhollyNewPullRequest(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)

	res, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("Created = %d, want 1", res.Created)
	}
	spec := dest.created[0]
	if spec.HeadBranch != "syncerd/pr/7" || spec.BaseBranch != "main" {
		t.Errorf("branches wrong: %+v", spec)
	}
	if !strings.Contains(spec.Body, Marker("acme/widget", 7)) {
		t.Error("the body must carry the marker")
	}
	if len(spec.Labels) != 1 || spec.Labels[0] != "bug" {
		t.Errorf("labels = %v", spec.Labels)
	}
	if _, ok := opts.State.GetPR("gh-to-gh", "acme/widget", 7); !ok {
		t.Error("state must record the mirrored pull request")
	}
}

func TestUnchangedPullRequestIsSkippedEntirely(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)
	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))

	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}
	before := dest.findCalls

	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.Created != 0 || res.Updated != 0 {
		t.Errorf("an unchanged pull request must produce no writes: %+v", res)
	}
	if dest.findCalls != before {
		t.Errorf("an unchanged pull request must not even look the destination up again")
	}
}

func TestChangedPullRequestUpdates(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)
	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))

	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	pr.Title = "Add login, take two"
	pr.UpdatedAt = pr.UpdatedAt.Add(time.Hour)

	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", res.Updated)
	}
	if dest.updated[0].Title != "Add login, take two" {
		t.Errorf("title not carried: %+v", dest.updated[0])
	}
}

func TestMergedSourceClosesWithANoteAndNeverMerges(t *testing.T) {
	dest := newFakeDest(t)
	conv := newFakeConv()
	opts := baseOpts(t, dest)
	opts.DestConv = conv
	opts.SourceConv = conv

	pr := openPR(7, time.Now())
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	merged := pr
	merged.State = vcs.PRMerged
	merged.MergeSHA = "deadbeef"
	merged.UpdatedAt = pr.UpdatedAt.Add(time.Hour)

	res, err := Sync(context.Background(), []vcs.PullRequest{merged}, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.Closed != 1 {
		t.Fatalf("Closed = %d, want 1", res.Closed)
	}
	if dest.mergeCalled {
		t.Fatal("a merged source must close the destination, never merge it")
	}

	var note string
	for _, p := range conv.posted {
		if strings.Contains(p, "merged") {
			note = p
		}
	}
	if note == "" {
		t.Fatal("closing a merged pull request must leave a note saying so")
	}
	if !strings.Contains(note, "deadbeef") {
		t.Errorf("the note must name the source merge commit: %q", note)
	}
	if !strings.Contains(note, "not merged here") {
		t.Errorf("the note must be honest that the destination is not merged: %q", note)
	}
}

func TestClosedSourceClosesTheDestination(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)

	pr := openPR(7, time.Now())
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	closed := pr
	closed.State = vcs.PRClosed
	closed.UpdatedAt = pr.UpdatedAt.Add(time.Hour)

	res, err := Sync(context.Background(), []vcs.PullRequest{closed}, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.Closed != 1 {
		t.Errorf("Closed = %d, want 1", res.Closed)
	}
	if last := dest.stateCalls[len(dest.stateCalls)-1]; last != vcs.PRClosed {
		t.Errorf("final state call = %q, want closed", last)
	}
}

func TestOpenSourceReopensADestinationClosedByHand(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)
	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))

	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Someone closes the mirror's pull request at the destination.
	rec, _ := opts.State.GetPR("gh-to-gh", "acme/widget", 7)
	rec.DestState = string(vcs.PRClosed)
	opts.State.MarkPR("gh-to-gh", "acme/widget", 7, rec)

	pr.UpdatedAt = pr.UpdatedAt.Add(time.Hour)
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("second: %v", err)
	}

	var reopened bool
	for _, s := range dest.stateCalls {
		if s == vcs.PROpen {
			reopened = true
		}
	}
	if !reopened {
		t.Error("the source is the authority: an open source must reopen the destination")
	}
}

func TestLostStateRecoversThroughTheDestinationRatherThanDuplicating(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)
	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))

	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}
	createdFirst := len(dest.created)

	// The state file is lost. The destination still holds the pull request.
	opts.State = state.NewGit()

	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(dest.created) != createdFirst {
		t.Fatalf("a lost state file must not create a duplicate pull request: %d creations", len(dest.created))
	}
	if res.Created != 0 {
		t.Errorf("Created = %d, want 0", res.Created)
	}
}

func TestClosedSourceNeverMirroredIsLeftAlone(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)

	pr := openPR(7, time.Now())
	pr.State = vcs.PRMerged

	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Created != 0 || len(dest.created) != 0 {
		t.Error("a pull request that finished before this mirror existed must not be created just to close it")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dest := newFakeDest(t)
	opts := baseOpts(t, dest)
	opts.DryRun = true

	res, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(dest.created) != 0 || len(dest.updated) != 0 || len(dest.stateCalls) != 0 {
		t.Error("a dry run must not write to the destination")
	}
	if res.Created != 1 {
		t.Errorf("a dry run must still report what it would do, Created = %d", res.Created)
	}
	if _, ok := opts.State.GetPR("gh-to-gh", "acme/widget", 7); ok {
		t.Error("a dry run must not record state")
	}
}

func TestOneFailingPullRequestDoesNotStopTheRest(t *testing.T) {
	dest := newFakeDest(t)
	dest.createErr = errors.New("422 validation failed")
	opts := baseOpts(t, dest)

	res, err := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now()), openPR(8, time.Now())}, opts)
	if err == nil {
		t.Fatal("a run with failures must report an error")
	}
	if len(res.Failures) != 2 {
		t.Errorf("both pull requests should be attempted and reported, got %d failures", len(res.Failures))
	}
}

func TestFailuresAreRedacted(t *testing.T) {
	dest := newFakeDest(t)
	dest.createErr = errors.New("401 with token ghp_supersecret")
	opts := baseOpts(t, dest)
	opts.Redact = func(s string) string { return strings.ReplaceAll(s, "ghp_supersecret", "****") }

	res, _ := Sync(context.Background(), []vcs.PullRequest{openPR(7, time.Now())}, opts)
	if len(res.Failures) != 1 {
		t.Fatalf("want one failure, got %d", len(res.Failures))
	}
	if strings.Contains(res.Failures[0], "ghp_supersecret") {
		t.Errorf("failure text must be redacted: %q", res.Failures[0])
	}
}

func TestDestinationThatCannotReopenLeavesItClosedAndSaysSo(t *testing.T) {
	inner := newFakeDest(t)
	opts := baseOpts(t, inner)
	opts.Dest = noReopenDest{inner: inner}

	// Compile time proof that this fake is the shape the test needs.
	if _, ok := opts.Dest.(vcs.PullRequestReopener); ok {
		t.Fatal("noReopenDest must not satisfy vcs.PullRequestReopener")
	}

	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Someone closes it at the destination while the source stays open.
	rec, _ := opts.State.GetPR("gh-to-gh", "acme/widget", 7)
	rec.DestState = string(vcs.PRClosed)
	opts.State.MarkPR("gh-to-gh", "acme/widget", 7, rec)

	pr.UpdatedAt = pr.UpdatedAt.Add(time.Hour)
	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("a destination that cannot reopen must not fail the run: %v", err)
	}
	if res.Created != 0 {
		t.Error("it must not open a second pull request for the same work")
	}

	rec, _ = opts.State.GetPR("gh-to-gh", "acme/widget", 7)
	if rec.DestState != destStateDivergent {
		t.Errorf("the divergence should be recorded, DestState = %q", rec.DestState)
	}
}

func TestRecordedDivergenceIsNotRetriedEveryRun(t *testing.T) {
	inner := newFakeDest(t)
	opts := baseOpts(t, inner)
	opts.Dest = noReopenDest{inner: inner}

	pr := openPR(7, time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))
	if _, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts); err != nil {
		t.Fatalf("first: %v", err)
	}
	rec, _ := opts.State.GetPR("gh-to-gh", "acme/widget", 7)
	rec.DestState = destStateDivergent
	opts.State.MarkPR("gh-to-gh", "acme/widget", 7, rec)

	before := len(inner.updated)
	res, err := Sync(context.Background(), []vcs.PullRequest{pr}, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(inner.updated) != before || res.Updated != 0 {
		t.Error("a divergence SyncerD cannot fix must not cost a write on every run")
	}
}
