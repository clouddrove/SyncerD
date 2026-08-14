package gitsync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/runreport"
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

// TestGitReportToRunReportClampsNegativeDuration reproduces a backward NTP
// step during a long run: EndedAt.Sub(StartedAt) goes negative because
// StartedAt is set with time.Now().UTC(), and .UTC() strips the monotonic
// clock reading Sub would otherwise use. The metrics writer already clamps
// this to zero, so the report must too, or the two outputs describing one
// run would disagree.
func TestGitReportToRunReportClampsNegativeDuration(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &GitReport{
		StartedAt: started,
		EndedAt:   started.Add(-5 * time.Second),
	}

	rr := r.ToRunReport("run-clamp", false)
	if rr.DurationSecs != 0 {
		t.Errorf("DurationSecs = %v, want 0 for an EndedAt before StartedAt", rr.DurationSecs)
	}
}

// TestCleanGitReportPlusRunErrorIsNotSuccess is the reproduction for a
// preflight or work directory lock failure: Engine.Run returns a report
// with no failures recorded (nothing was ever attempted), yet the process
// exits non-zero. ToRunReport alone reports Success: true for that
// report, same as TestGitReportToRunReportCleanRunIsSuccess above; this
// test proves that feeding the run error through runreport.WriteRun
// corrects it to Success: false in the file that actually gets written.
func TestCleanGitReportPlusRunErrorIsNotSuccess(t *testing.T) {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	r := &GitReport{
		StartedAt: started,
		EndedAt:   started.Add(1 * time.Second),
		// No Mirrored, no Failures: exactly what Engine.Run returns when
		// Preflight or the work directory lock fails before any mirror
		// is attempted.
	}

	rr := r.ToRunReport("run-3", false)
	if !rr.Success {
		t.Fatal("sanity check: ToRunReport alone should still say Success: true for an empty report")
	}

	path := filepath.Join(t.TempDir(), "report.json")
	runErr := errors.New("acquire lock: permission denied")
	if err := runreport.WriteRun(path, rr, runErr); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got runreport.Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Success {
		t.Error("Success should be false: the run aborted before any mirror was attempted")
	}
	if len(got.Failures) != 0 {
		t.Errorf("expected Failures to remain empty, got %+v", got.Failures)
	}
}
