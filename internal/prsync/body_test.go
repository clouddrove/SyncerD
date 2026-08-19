package prsync

import (
	"strings"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func TestMarkerIsStable(t *testing.T) {
	if got := Marker("acme/widget", 7); got != "<!-- syncerd:pr:acme/widget#7 -->" {
		t.Errorf("Marker = %q", got)
	}
	if !HasMarker("prefix\n"+Marker("acme/widget", 7)+"\nbody", Marker("acme/widget", 7)) {
		t.Error("HasMarker should find a marker anywhere in the body")
	}
	if HasMarker("no marker here", Marker("acme/widget", 7)) {
		t.Error("HasMarker matched a body that has no marker")
	}
}

func TestNeutraliseStopsMentionsAndCrossLinks(t *testing.T) {
	got := Neutralise("thanks @alice, see #12 and also @bob-2")

	if strings.Contains(got, "@alice") {
		t.Errorf("a live mention survived: %q", got)
	}
	if strings.Contains(got, "#12") {
		t.Errorf("a live issue reference survived: %q", got)
	}
	// The text must still read the same once the zero width joiner is
	// stripped, since the point is to keep the prose intact.
	if stripped := strings.ReplaceAll(got, zwj, ""); stripped != "thanks @alice, see #12 and also @bob-2" {
		t.Errorf("text changed beyond the joiner: %q", stripped)
	}
}

func TestNeutraliseLeavesEmailAddressesAlone(t *testing.T) {
	got := Neutralise("mail ops@example.com for access")
	if strings.Contains(got, zwj) {
		t.Errorf("an email address must not be rewritten: %q", got)
	}
}

func TestNeutraliseLeavesFencedCodeAlone(t *testing.T) {
	in := "before @alice\n```\ncurl -H 'x: @alice' host/#12\n```\nafter #12"
	got := Neutralise(in)

	fence := got[strings.Index(got, "```"):strings.LastIndex(got, "```")]
	if strings.Contains(fence, zwj) {
		t.Errorf("code inside a fence must survive byte for byte: %q", fence)
	}
	if !strings.Contains(strings.SplitN(got, "```", 2)[0], zwj) {
		t.Error("text before the fence should still be neutralised")
	}
	if !strings.Contains(got[strings.LastIndex(got, "```"):], zwj) {
		t.Error("text after the fence should still be neutralised")
	}
}

func samplePR() vcs.PullRequest {
	return vcs.PullRequest{
		Number:    7,
		Title:     "Add login",
		Body:      "closes #4, thanks @alice",
		Author:    vcs.Actor{Handle: "outsider"},
		WebURL:    "https://github.com/acme/widget/pull/7",
		CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
}

func TestComposeBodyCarriesMarkerAttributionAndSafeText(t *testing.T) {
	got := ComposeBody(samplePR(), "acme/widget")

	if !strings.HasPrefix(got, Marker("acme/widget", 7)) {
		t.Errorf("body must start with the marker: %q", got)
	}
	if !strings.Contains(got, "https://github.com/acme/widget/pull/7") {
		t.Error("body must link to the source pull request")
	}
	if !strings.Contains(strings.ReplaceAll(got, zwj, ""), "@outsider") {
		t.Error("body must name the real author")
	}
	if !strings.Contains(got, "2026-08-01") {
		t.Error("body must carry the source creation date")
	}
	if strings.Contains(got, "@alice") || strings.Contains(got, "#4") {
		t.Errorf("the mirrored body must not notify or cross link: %q", got)
	}
}

func TestComposeBodyIsIdempotent(t *testing.T) {
	pr := samplePR()
	once := ComposeBody(pr, "acme/widget")

	// A later run re-renders from the same source. Feeding the composed
	// body back in must not stack a second header.
	pr.Body = once
	twice := ComposeBody(pr, "acme/widget")

	if strings.Count(twice, "Mirrored from") != 1 {
		t.Errorf("header stacked on recomposition:\n%s", twice)
	}
	if strings.Count(twice, Marker("acme/widget", 7)) != 1 {
		t.Errorf("marker stacked on recomposition:\n%s", twice)
	}
}

func TestComposeCommentAttributesAndNeutralises(t *testing.T) {
	got := ComposeComment(vcs.Comment{
		SourceID:  "991",
		Author:    vcs.Actor{Handle: "reviewer"},
		Body:      "ping @carol about #9",
		CreatedAt: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
	}, "acme/widget", 7)

	if !strings.HasPrefix(got, CommentMarker("acme/widget", 7, "991")) {
		t.Errorf("comment must start with its marker: %q", got)
	}
	if !strings.Contains(strings.ReplaceAll(got, zwj, ""), "@reviewer") {
		t.Error("comment must name the real author")
	}
	if strings.Contains(got, "@carol") || strings.Contains(got, "#9") {
		t.Errorf("mirrored comment must not notify or cross link: %q", got)
	}
	if !strings.Contains(got, "2026-08-02") {
		t.Error("comment must carry the source date")
	}
}

func TestComposeReviewSaysItIsNotAnApproval(t *testing.T) {
	got := ComposeReview(vcs.Review{
		SourceID:    "55",
		Author:      vcs.Actor{Handle: "maintainer"},
		State:       "approved",
		Body:        "looks good",
		SubmittedAt: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
	}, "acme/widget", 7)

	if !strings.Contains(got, "approved") {
		t.Error("the verdict must be named")
	}
	if !strings.Contains(got, "not an approval here") {
		t.Error("a mirrored verdict must say plainly that it is not a destination approval")
	}
	if !strings.Contains(got, "looks good") {
		t.Error("the review body must be carried")
	}
}

func TestComposeReviewWithoutABody(t *testing.T) {
	got := ComposeReview(vcs.Review{
		SourceID: "56", Author: vcs.Actor{Handle: "maintainer"}, State: "changes_requested",
		SubmittedAt: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
	}, "acme/widget", 7)

	if !strings.Contains(got, "changes requested") {
		t.Errorf("verdict label should read as prose: %q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Error("an empty review body must not leave trailing blank lines")
	}
}

func TestAttributionFallsBackWhenTheAuthorIsUnknown(t *testing.T) {
	if got := attribution(vcs.Actor{}); got != "an unknown author" {
		t.Errorf("attribution = %q", got)
	}
	if got := attribution(vcs.Actor{DisplayName: "Alice A"}); !strings.Contains(got, "Alice A") {
		t.Errorf("attribution should fall back to the display name, got %q", got)
	}
}
