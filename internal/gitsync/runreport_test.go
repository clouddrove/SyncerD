package gitsync

import (
	"testing"
	"time"
)

func TestGitReportToRunReportConversion(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &GitReport{
		StartedAt: started,
		EndedAt:   started.Add(20 * time.Second),
		Mirrored: []MirrorEvent{
			{Mirror: "gh-to-gl", SourceRepo: "acme/app", DestRepo: "acme/app", RefsPushed: 5, RefsDeleted: 1, Created: true},
			{Mirror: "gh-to-gl", SourceRepo: "acme/lib", DestRepo: "acme/lib", RefsPushed: 2, RefsDeleted: 0},
		},
		Skipped: 3,
		Failures: []GitFailure{
			{Mirror: "gh-to-gl", SourceRepo: "acme/broken", DestRepo: "acme/broken", Stage: "push", Error: "connection refused"},
		},
	}

	rr := r.ToRunReport("run-1", false)

	if rr.Command != "git-sync" {
		t.Errorf("Command = %q, want git-sync", rr.Command)
	}
	if rr.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", rr.RunID)
	}
	if rr.Success {
		t.Error("Success should be false when there are failures")
	}
	if rr.Counts.Succeeded != 2 {
		t.Errorf("Counts.Succeeded = %d, want 2", rr.Counts.Succeeded)
	}
	if rr.Counts.Skipped != 3 {
		t.Errorf("Counts.Skipped = %d, want 3", rr.Counts.Skipped)
	}
	if rr.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", rr.Counts.Failed)
	}
	if len(rr.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(rr.Items))
	}
	item := rr.Items[0]
	if item.Group != "gh-to-gl" || item.Source != "acme/app" || item.Destination != "acme/app" || !item.Created {
		t.Errorf("Items[0] mapped wrong: %+v", item)
	}
	if item.Detail["refs_pushed"] != 5 || item.Detail["refs_deleted"] != 1 {
		t.Errorf("Items[0].Detail mapped wrong: %+v", item.Detail)
	}
	if len(rr.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(rr.Failures))
	}
	f := rr.Failures[0]
	if f.Group != "gh-to-gl" || f.Source != "acme/broken" || f.Destination != "acme/broken" || f.Stage != "push" || f.Error != "connection refused" {
		t.Errorf("Failures[0] mapped wrong: %+v", f)
	}
	if rr.DurationSecs != 20 {
		t.Errorf("DurationSecs = %v, want 20", rr.DurationSecs)
	}
}

func TestGitReportToRunReportCleanRunIsSuccess(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &GitReport{
		StartedAt: started,
		EndedAt:   started.Add(1 * time.Second),
		Mirrored: []MirrorEvent{
			{Mirror: "gh-to-gl", SourceRepo: "acme/app", DestRepo: "acme/app", RefsPushed: 1},
		},
	}

	rr := r.ToRunReport("run-2", true)

	if !rr.Success {
		t.Error("Success should be true when there are no failures")
	}
	if !rr.DryRun {
		t.Error("DryRun should carry through from the argument")
	}
	if len(rr.Failures) != 0 {
		t.Errorf("expected no failures, got %+v", rr.Failures)
	}
}
