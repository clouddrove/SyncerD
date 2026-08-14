package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/gitsync"
	"github.com/clouddrove/syncerd/internal/sync"
)

// TestWriteSyncMetricsEmptyPathWritesNothing mirrors runreport.WriteRun's
// contract: --metrics-file is optional, so an empty path must be a no-op.
func TestWriteSyncMetricsEmptyPathWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")

	report := &sync.Report{StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}
	writeSyncMetrics("", report, nil)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file written for an empty path, stat err = %v", err)
	}
}

// TestWriteSyncMetricsWritesParseableFile checks that a set path produces a
// file carrying the command name and a success series for a run with no
// error and no failures.
func TestWriteSyncMetricsWritesParseableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")
	started := time.Now().UTC()

	report := &sync.Report{
		StartedAt: started,
		EndedAt:   started.Add(2 * time.Second),
		NewSyncs:  []sync.SyncEvent{{Destination: "ecr", Image: "library/nginx", Tag: "1.25"}},
		Skipped:   1,
	}
	writeSyncMetrics(path, report, nil)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, `command="sync"`) {
		t.Errorf("expected a command=%q series, got:\n%s", "sync", content)
	}
	if !strings.Contains(content, `syncerd_last_run_success{command="sync"} 1`) {
		t.Errorf("expected a successful run to report success=1, got:\n%s", content)
	}
	if !strings.Contains(content, `syncerd_last_run_items{command="sync",result="succeeded"} 1`) {
		t.Errorf("expected succeeded=1, got:\n%s", content)
	}
}

// TestWriteSyncMetricsFailedRunStillWritesFile checks the case the task
// called out explicitly: a failed run must still update
// syncerd_last_run_unixtime and flip syncerd_last_run_success to 0, rather
// than silently skipping the write.
func TestWriteSyncMetricsFailedRunStillWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")
	started := time.Now().UTC()

	report := &sync.Report{StartedAt: started, EndedAt: started.Add(time.Second)}
	writeSyncMetrics(path, report, errors.New("sync failed"))

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, `syncerd_last_run_success{command="sync"} 0`) {
		t.Errorf("expected a failed run to report success=0, got:\n%s", content)
	}
	if !strings.Contains(content, `syncerd_last_run_unixtime{command="sync"}`) {
		t.Errorf("expected a failed run to still update syncerd_last_run_unixtime, got:\n%s", content)
	}
}

// TestWriteGitMetricsCoexistsWithSyncSeries proves sync and git-sync can
// share one metrics file: WriteTextfile preserves any other command's
// series, so writing git-sync metrics after sync metrics must not drop the
// sync series.
func TestWriteGitMetricsCoexistsWithSyncSeries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")
	started := time.Now().UTC()

	syncReport := &sync.Report{StartedAt: started, EndedAt: started.Add(time.Second)}
	writeSyncMetrics(path, syncReport, nil)

	gitReport := &gitsync.GitReport{
		StartedAt: started,
		EndedAt:   started.Add(time.Second),
		Mirrored:  []gitsync.MirrorEvent{{Mirror: "gh-to-gl", RefsPushed: 3}},
	}
	writeGitMetrics(path, gitReport, nil)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, `command="sync"`) {
		t.Errorf("expected the earlier sync series to be preserved, got:\n%s", content)
	}
	if !strings.Contains(content, `command="git-sync"`) {
		t.Errorf("expected a git-sync series to be present, got:\n%s", content)
	}
}

// TestWriteGitMetricsEmptyPathWritesNothing mirrors the sync side.
func TestWriteGitMetricsEmptyPathWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")

	report := &gitsync.GitReport{StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}
	writeGitMetrics("", report, nil)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file written for an empty path, stat err = %v", err)
	}
}
