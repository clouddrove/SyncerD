package sync

import (
	"testing"
	"time"
)

func TestReportToRunReportConversion(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &Report{
		StartedAt: started,
		EndedAt:   started.Add(10 * time.Second),
		NewSyncs: []SyncEvent{
			{Destination: "ecr", Image: "library/nginx", Tag: "1.25", Ref: "ecr.example.com/library/nginx:1.25"},
			{Destination: "acr", Image: "library/redis", Tag: "7", Ref: "acr.example.com/library/redis:7"},
		},
		Skipped: 4,
		Failures: []FailureEvent{
			{Destination: "gcr", Image: "library/redis", Tag: "7", Ref: "gcr.example.com/library/redis:7", Error: "denied"},
		},
	}

	rr := r.ToRunReport("run-1", false)

	if rr.SchemaVersion == 0 {
		t.Error("SchemaVersion not set")
	}
	if rr.Command != "sync" {
		t.Errorf("Command = %q, want sync", rr.Command)
	}
	if rr.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", rr.RunID)
	}
	if rr.DryRun {
		t.Error("DryRun should be false for image sync")
	}
	if rr.Success {
		t.Error("Success should be false when there are failures")
	}
	if rr.Counts.Succeeded != 2 {
		t.Errorf("Counts.Succeeded = %d, want 2", rr.Counts.Succeeded)
	}
	if rr.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", rr.Counts.Failed)
	}
	if rr.Counts.Skipped != 4 {
		t.Errorf("Counts.Skipped = %d, want 4", rr.Counts.Skipped)
	}
	if len(rr.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(rr.Items))
	}
	if rr.Items[0].Group != "ecr" || rr.Items[0].Source != "library/nginx:1.25" || rr.Items[0].Destination != "ecr.example.com/library/nginx:1.25" {
		t.Errorf("Items[0] mapped wrong: %+v", rr.Items[0])
	}
	if len(rr.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(rr.Failures))
	}
	f := rr.Failures[0]
	if f.Group != "gcr" || f.Source != "library/redis:7" || f.Destination != "gcr.example.com/library/redis:7" || f.Error != "denied" {
		t.Errorf("Failures[0] mapped wrong: %+v", f)
	}
	if f.Stage != "" {
		t.Errorf("Failures[0].Stage = %q, want empty", f.Stage)
	}
	if rr.DurationSecs != 10 {
		t.Errorf("DurationSecs = %v, want 10", rr.DurationSecs)
	}
}

func TestReportToRunReportCleanRunIsSuccess(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &Report{
		StartedAt: started,
		EndedAt:   started.Add(2 * time.Second),
		NewSyncs: []SyncEvent{
			{Destination: "ecr", Image: "library/nginx", Tag: "1.25", Ref: "ecr.example.com/library/nginx:1.25"},
		},
	}

	rr := r.ToRunReport("run-2", false)

	if !rr.Success {
		t.Error("Success should be true when there are no failures")
	}
	if len(rr.Failures) != 0 {
		t.Errorf("expected no failures, got %+v", rr.Failures)
	}
}

// TestReportToRunReportClampsNegativeDuration reproduces a backward NTP
// step during a long run: EndedAt.Sub(StartedAt) goes negative because
// StartedAt is set with time.Now().UTC(), and .UTC() strips the monotonic
// clock reading Sub would otherwise use. The metrics writer already clamps
// this to zero, so the report must too, or the two outputs describing one
// run would disagree.
func TestReportToRunReportClampsNegativeDuration(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &Report{
		StartedAt: started,
		EndedAt:   started.Add(-5 * time.Second),
	}

	rr := r.ToRunReport("run-clamp", false)
	if rr.DurationSecs != 0 {
		t.Errorf("DurationSecs = %v, want 0 for an EndedAt before StartedAt", rr.DurationSecs)
	}
}
