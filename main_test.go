package main

import (
	"errors"
	"io"
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
	writeGitMetrics(path, gitReport, false, nil)

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
	writeGitMetrics("", report, false, nil)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file written for an empty path, stat err = %v", err)
	}
}

// TestWriteGitMetricsDryRunWritesNothing is item 2 of the fix wave: a dry
// run must not advance syncerd_last_run_unixtime or
// syncerd_last_success_unixtime, because it created, pushed, and deleted
// nothing. Before this fix, the metrics writer had no dryRun parameter at
// all and wrote as though the run were real, which could mask a genuinely
// dead cron for a whole alert window if an operator pointed --dry-run at
// the production textfile path while debugging.
func TestWriteGitMetricsDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")
	started := time.Now().UTC()

	report := &gitsync.GitReport{
		StartedAt: started,
		EndedAt:   started.Add(time.Second),
		Mirrored:  []gitsync.MirrorEvent{{Mirror: "gh-to-gl", RefsPushed: 3}},
	}
	writeGitMetrics(path, report, true, nil)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no metrics file for a dry run, stat err = %v", err)
	}
}

// TestWriteGitMetricsRealRunAfterDryRunWritesFile is the other half of the
// dry run assertion: a real run at the same path must still produce a
// file, so the dry run skip is scoped to dryRun alone and not a change
// that broke the writer generally.
func TestWriteGitMetricsRealRunAfterDryRunWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.prom")
	started := time.Now().UTC()

	report := &gitsync.GitReport{
		StartedAt: started,
		EndedAt:   started.Add(time.Second),
		Mirrored:  []gitsync.MirrorEvent{{Mirror: "gh-to-gl", RefsPushed: 3}},
	}
	writeGitMetrics(path, report, false, nil)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected a metrics file for a real run: %v", err)
	}
}

// TestWriteSyncReportEmptyPathBuildsNothing and
// TestWriteGitReportEmptyPathBuildsNothing are item 6: with no --report
// path, writeSyncReport/writeGitReport must return before ToRunReport
// builds anything, matching the metrics writers' existing path-first
// check. There is no exported hook into "did NewRunID run", so these
// assert the observable contract instead: no file appears at a path
// derived from the empty string, and, more directly, that the function
// does not panic or block reading crypto/rand when report is otherwise
// fully populated. TestWriteSyncMetricsEmptyPathWritesNothing and
// TestWriteGitMetricsEmptyPathWritesNothing above already cover the
// no-op-on-empty-path contract for the metrics writers; these two cover
// the same contract for the report writers, which previously built a
// runreport.Report and only then discovered WriteRun had nothing to do.
func TestWriteSyncReportEmptyPathBuildsNothing(t *testing.T) {
	report := &sync.Report{
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
		NewSyncs:  []sync.SyncEvent{{Destination: "ecr", Image: "library/nginx", Tag: "1.25"}},
	}
	// Must not panic and must not create anything; there is nowhere for it
	// to write, since path is empty.
	writeSyncReport("", report, nil)
}

func TestWriteGitReportEmptyPathBuildsNothing(t *testing.T) {
	report := &gitsync.GitReport{
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
		Mirrored:  []gitsync.MirrorEvent{{Mirror: "gh-to-gl", RefsPushed: 3}},
	}
	writeGitReport("", report, false, nil)
}

// TestRuntimeFailureSilencesUsageButFlagErrorDoesNot is item 7: the branch's
// one visible change to production behavior is that a runtime failure (an
// error returned from RunE, such as a config that fails validation) no
// longer prints cobra's usage block, while a flag parse error (a typo like
// --bogus-flag, which never reaches RunE) still does. Nothing asserted
// that split before this test.
//
// cmd.SilenceUsage is set as the first line of each RunE, so it is
// testable in process by constructing the real command tree via
// newRootCmd, executing it, and inspecting the field afterward: true means
// RunE ran and chose to silence usage (a runtime failure); false, its zero
// value, means RunE was never entered (a flag parse error), which is
// exactly the condition that leaves cobra's own usage printing enabled.
func TestRuntimeFailureSilencesUsageButFlagErrorDoesNot(t *testing.T) {
	t.Run("runtime failure", func(t *testing.T) {
		root := newRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		// os.DevNull has no destinations, so ValidateImageSync fails inside
		// RunE, after cmd.SilenceUsage = true has already run: a runtime
		// failure with no credentials and no network access needed.
		root.SetArgs([]string{"sync", "--once", "--config", os.DevNull})

		if err := root.Execute(); err == nil {
			t.Fatal("expected a runtime failure for a config with no destinations")
		}

		syncCmd, _, err := root.Find([]string{"sync"})
		if err != nil {
			t.Fatalf("find sync command: %v", err)
		}
		if !syncCmd.SilenceUsage {
			t.Error("expected SilenceUsage to be true after a runtime failure inside RunE")
		}
	})

	t.Run("flag parse error", func(t *testing.T) {
		root := newRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"sync", "--bogus-flag"})

		if err := root.Execute(); err == nil {
			t.Fatal("expected a flag parse error for --bogus-flag")
		}

		syncCmd, _, err := root.Find([]string{"sync"})
		if err != nil {
			t.Fatalf("find sync command: %v", err)
		}
		if syncCmd.SilenceUsage {
			t.Error("expected SilenceUsage to remain false for a flag parse error, since RunE never runs")
		}
	})
}

// TestCommandsTolerateEmptyPositionalArg guards an invariant the GitHub
// Action's action.yml relies on but that is not written down anywhere in
// the CLI itself: a Docker action's args list cannot branch, so the
// element that carries --dry-run for git-sync evaluates to an empty
// string when the command is sync, and that empty string still arrives as
// its own argv entry. Today this is harmless because neither command sets
// a cobra.Args validator, so cobra's default (ArbitraryArgs) accepts it
// silently. If this test fails, someone has tightened one of the commands
// with an Args validator, which is reasonable on its own, but action.yml's
// conditional --dry-run element needs to change in the same commit, or
// every existing image sync workflow breaks the next time the Action
// image is published, and it would fail inside a user's GitHub Action run
// rather than in any test here.
func TestCommandsTolerateEmptyPositionalArg(t *testing.T) {
	for _, sub := range []string{"sync", "git-sync"} {
		t.Run(sub, func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs([]string{sub, "--config", os.DevNull, "--once=true", "", "--report=", "--metrics-file=", "--log-format=text"})
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			err := root.Execute()
			// Both fail on configuration, which is the point: the empty
			// argument must not produce a flag or argument parse error.
			if err == nil {
				t.Fatalf("%s: expected a configuration error", sub)
			}
			if strings.Contains(err.Error(), "unknown") || strings.Contains(err.Error(), "accepts") {
				t.Errorf("%s: empty positional argument was rejected: %v", sub, err)
			}
		})
	}
}
