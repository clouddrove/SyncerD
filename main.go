package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/gitsync"
	"github.com/clouddrove/syncerd/internal/logging"
	"github.com/clouddrove/syncerd/internal/metrics"
	"github.com/clouddrove/syncerd/internal/runreport"
	"github.com/clouddrove/syncerd/internal/sync"
	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		// This is the CLI's own error reporting for the process exit, not
		// run logging, but it still goes through the logger so a json
		// stream parses end to end: the last line of a failed run must be
		// the same shape as every line before it.
		logging.Error(err.Error())
		os.Exit(1)
	}
}

// newRootCmd builds the command tree fresh: root, sync, and git-sync, with
// every flag and RunE wired up, but does not execute it. Split out of main
// so a test can construct the tree, run it in process against a real or
// synthetic config, and inspect cobra state afterward, such as whether a
// RunE runtime failure set cmd.SilenceUsage while a flag parse error left
// it at its zero value. main itself only needs the two lines above.
func newRootCmd() *cobra.Command {
	var rootCmd = &cobra.Command{
		Use:   "syncerd",
		Short: "Your lightweight artifact sync engine",
		Long: `SyncerD - Your lightweight artifact sync engine.

SyncerD synchronizes Docker images from Docker Hub to other container
registries (ECR, ACR, GCR, GitHub Container Registry), and mirrors git
repositories across GitHub, GitLab, Bitbucket, Azure DevOps, and AWS
CodeCommit, in any direction.`,
		Version: fmt.Sprintf("%s (commit: %s)", version, commit),
		// SilenceErrors: cobra's own "Error: ..." line is never wanted; the
		// root error is reported once, through the logger, at the bottom of
		// main, so a json log stream parses end to end. SilenceUsage is
		// deliberately NOT set here: a flag parse error (a typo like
		// --bogus-flag) never reaches a command's RunE, so the usage block
		// below still fires for those. Each RunE sets cmd.SilenceUsage
		// itself, once parsing has already succeeded, so only a genuine
		// runtime failure skips it.
		SilenceErrors: true,
	}

	var cfgFile string
	var logFormat string
	var metricsFile string
	var runOnce bool
	var syncReportPath string

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./syncerd.yaml)")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", logging.FormatText, "log output format: text or json")
	rootCmd.PersistentFlags().StringVar(&metricsFile, "metrics-file", "", "write Prometheus textfile collector metrics for the last run to this path")

	// PersistentPreRunE configures the logger before any command body runs,
	// so an unknown --log-format fails loudly at startup rather than
	// silently logging in the wrong shape partway through a run.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := logging.Setup(logFormat, os.Stderr); err != nil {
			return err
		}
		return nil
	}

	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync images according to configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag parsing has already succeeded by the time RunE is
			// entered, so any error from here is a runtime failure and the
			// flag list is noise. Parse errors still print usage, because
			// this line is never reached for them.
			cmd.SilenceUsage = true

			cfg, err := config.Load(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if err := cfg.ValidateImageSync(); err != nil {
				return err
			}

			syncer, err := sync.NewSyncer(cfg)
			if err != nil {
				return fmt.Errorf("failed to create syncer: %w", err)
			}

			if runOnce {
				logging.Info("Running sync once...")
				report, err := syncer.SyncAll(context.Background())
				writeSyncReport(syncReportPath, report, err)
				writeSyncMetrics(metricsFile, report, err)
				return err
			}

			// Run with cron scheduler
			if cfg.Schedule == "" {
				cfg.Schedule = "0 0 */21 * *" // Every 3 weeks (21 days)
			}

			logging.Info(fmt.Sprintf("Starting SyncerD with schedule: %s", cfg.Schedule), "schedule", cfg.Schedule)
			return runWithCron(cfg, syncer, syncReportPath, metricsFile)
		},
	}

	syncCmd.Flags().BoolVar(&runOnce, "once", false, "run sync once and exit (default: false, runs continuously with cron)")
	syncCmd.Flags().StringVar(&syncReportPath, "report", "", "write a machine readable JSON report of the run to this path")
	rootCmd.AddCommand(syncCmd)

	var gitOnce bool
	var gitDryRun bool
	var gitReportPath string

	gitSyncCmd := &cobra.Command{
		Use:   "git-sync",
		Short: "Mirror git repositories between hosting providers",
		Long: `Mirror git repositories between GitHub, GitLab, Bitbucket, Azure DevOps,
and AWS CodeCommit, in any direction.

Repositories are discovered from the source provider, filtered, and mirrored
as a full replica of all branches and tags. Unchanged repositories are
skipped without cloning.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag parsing has already succeeded by the time RunE is
			// entered, so any error from here is a runtime failure and the
			// flag list is noise. Parse errors still print usage, because
			// this line is never reached for them.
			cmd.SilenceUsage = true

			cfg, err := config.Load(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			if err := cfg.ValidateGitSync(); err != nil {
				return err
			}

			syncer, err := gitsync.NewSyncer(cfg, gitDryRun)
			if err != nil {
				return fmt.Errorf("failed to create git syncer: %w", err)
			}

			if gitOnce || gitDryRun {
				if gitDryRun {
					logging.Info("Dry run: no repository will be created or pushed")
				}
				report, err := syncer.SyncAll(context.Background())
				writeGitReport(gitReportPath, report, gitDryRun, err)
				writeGitMetrics(metricsFile, report, gitDryRun, err)
				return err
			}

			logging.Info(fmt.Sprintf("Starting SyncerD git-sync with schedule: %s", cfg.Git.Schedule), "schedule", cfg.Git.Schedule)
			return runGitCron(cfg, syncer, gitReportPath, metricsFile)
		},
	}

	gitSyncCmd.Flags().BoolVar(&gitOnce, "once", false, "run the mirror once and exit (default: false, runs continuously with cron)")
	gitSyncCmd.Flags().BoolVar(&gitDryRun, "dry-run", false, "report what would be created, pushed, and pruned without writing anything")
	gitSyncCmd.Flags().StringVar(&gitReportPath, "report", "", "write a machine readable JSON report of the run to this path")
	rootCmd.AddCommand(gitSyncCmd)

	return rootCmd
}

// writeSyncReport converts report and writes it as JSON to path, when path
// is set. runErr is the error SyncAll returned for this run: it overrides
// the converted report's success field, since a run can abort (for
// example, a state save failure) before any per artifact failure is
// recorded. A write failure is logged and otherwise ignored: the run
// itself already happened, and a report that could not be written must
// not turn a successful sync into a failed process exit.
//
// The path check comes before ToRunReport so an unset --report costs
// nothing: no run ID (crypto/rand), no slice or map allocation for a
// value that would be thrown away.
func writeSyncReport(path string, report *sync.Report, runErr error) {
	if path == "" || report == nil {
		return
	}
	// Image sync has no dry run mode today.
	rr := report.ToRunReport(runreport.NewRunID(), false)
	if err := runreport.WriteRun(path, rr, runErr); err != nil {
		logging.Error(fmt.Sprintf("failed to write run report to %s: %v", path, err),
			"path", path, "error", err.Error())
	}
}

// writeGitReport converts report and writes it as JSON to path, when path
// is set. runErr is the error SyncAll returned for this run: it overrides
// the converted report's success field, since a run can abort (for
// example, a preflight or work directory lock failure) before any per
// artifact failure is recorded. A write failure is logged and otherwise
// ignored, the same way Slack failures are already best effort.
//
// The path check comes before ToRunReport so an unset --report costs
// nothing: no run ID (crypto/rand), no slice or map allocation for a
// value that would be thrown away.
func writeGitReport(path string, report *gitsync.GitReport, dryRun bool, runErr error) {
	if path == "" || report == nil {
		return
	}
	rr := report.ToRunReport(runreport.NewRunID(), dryRun)
	if err := runreport.WriteRun(path, rr, runErr); err != nil {
		logging.Error(fmt.Sprintf("failed to write run report to %s: %v", path, err),
			"path", path, "error", err.Error())
	}
}

// writeSyncMetrics writes a Prometheus textfile collector file describing
// this run to path, when path is set. It shares runErr's role with
// writeSyncReport: a run can abort before any per artifact failure lands in
// report.Failures, so runErr is the ground truth for Success, honestly
// reported either way. A failed run still writes a metrics file: that is
// how syncerd_last_run_success flips to 0 and syncerd_last_run_unixtime
// advances, which is the whole point of the metric. A write failure is
// logged and otherwise ignored, exactly like the report writer: a metrics
// problem must never fail the run.
func writeSyncMetrics(path string, report *sync.Report, runErr error) {
	if path == "" || report == nil {
		return
	}
	m := metrics.RunMetrics{
		Command:   "sync",
		Success:   runErr == nil && len(report.Failures) == 0,
		StartedAt: report.StartedAt,
		EndedAt:   report.EndedAt,
		Succeeded: len(report.NewSyncs),
		Skipped:   report.Skipped,
		Failed:    len(report.Failures),
	}
	if err := metrics.WriteTextfile(path, m); err != nil {
		logging.Error(fmt.Sprintf("failed to write metrics to %s: %v", path, err),
			"path", path, "error", err.Error())
	}
}

// writeGitMetrics is writeSyncMetrics for git-sync; see there for the
// rationale shared by both. dryRun is a hard skip: a dry run creates,
// pushes, and deletes nothing, so it must not advance
// syncerd_last_run_unixtime or syncerd_last_success_unixtime either. An
// operator debugging with --dry-run against the production textfile path
// would otherwise mask a genuinely dead cron for a whole alert window,
// since the metrics would say the most recent run "succeeded" just now.
// A dry_run label was considered and rejected: the staleness alert
// (time() - syncerd_last_success_unixtime{command="git-sync"} > threshold)
// selects on command alone, and a second series under that command would
// either break the query or require it to grow a label matcher it should
// never have needed. Writing nothing is simpler and correct.
func writeGitMetrics(path string, report *gitsync.GitReport, dryRun bool, runErr error) {
	if path == "" || report == nil || dryRun {
		return
	}
	m := metrics.RunMetrics{
		Command:   "git-sync",
		Success:   runErr == nil && len(report.Failures) == 0,
		StartedAt: report.StartedAt,
		EndedAt:   report.EndedAt,
		Succeeded: len(report.Mirrored),
		Skipped:   report.Skipped,
		Failed:    len(report.Failures),
	}
	if err := metrics.WriteTextfile(path, m); err != nil {
		logging.Error(fmt.Sprintf("failed to write metrics to %s: %v", path, err),
			"path", path, "error", err.Error())
	}
}

func runWithCron(cfg *config.Config, syncer *sync.Syncer, reportPath, metricsPath string) error {
	c := cron.New(cron.WithLocation(time.UTC))

	// Run immediately on startup
	logging.Info("Running initial sync...")
	report, err := syncer.SyncAll(context.Background())
	writeSyncReport(reportPath, report, err)
	writeSyncMetrics(metricsPath, report, err)
	if err != nil {
		logging.Error(fmt.Sprintf("Initial sync error: %v", err), "error", err.Error())
	}

	// Schedule periodic syncs. Each run gets its own report, overwriting
	// the file from the previous run, so it always describes the most
	// recent one.
	_, err = c.AddFunc(cfg.Schedule, func() {
		logging.Info("Running scheduled sync...")
		report, err := syncer.SyncAll(context.Background())
		writeSyncReport(reportPath, report, err)
		writeSyncMetrics(metricsPath, report, err)
		if err != nil {
			logging.Error(fmt.Sprintf("Scheduled sync error: %v", err), "error", err.Error())
		}
	})
	if err != nil {
		return fmt.Errorf("invalid cron schedule: %w", err)
	}

	c.Start()
	logging.Info("Cron scheduler started. Waiting for signals...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logging.Info("Shutting down...")
	ctx := c.Stop()
	<-ctx.Done()
	return nil
}

func runGitCron(cfg *config.Config, syncer *gitsync.Syncer, reportPath, metricsPath string) error {
	// Skip a tick rather than start a second run on the same Engine. A
	// first mirror of a large org can outlive its own interval, and Run
	// keeps per run state on the receiver, so an overlapping run would
	// reset that state mid flight.
	c := cron.New(
		cron.WithLocation(time.UTC),
		cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)),
	)

	// Validate the schedule before spending a full mirror run, so a typo
	// in git.schedule is caught immediately instead of after the first run
	// completes.
	//
	// gitDryRun is never true here: the RunE handler above takes the
	// --once/--dry-run branch and never reaches runGitCron in that case,
	// so every report written from the cron loop describes a real run.
	_, err := c.AddFunc(cfg.Git.Schedule, func() {
		logging.Info("Running scheduled git mirror...")
		report, err := syncer.SyncAll(context.Background())
		writeGitReport(reportPath, report, false, err)
		writeGitMetrics(metricsPath, report, false, err)
		if err != nil {
			logging.Error(fmt.Sprintf("Scheduled git mirror error: %v", err), "error", err.Error())
		}
	})
	if err != nil {
		return fmt.Errorf("invalid git cron schedule: %w", err)
	}

	logging.Info("Running initial git mirror...")
	report, err := syncer.SyncAll(context.Background())
	writeGitReport(reportPath, report, false, err)
	writeGitMetrics(metricsPath, report, false, err)
	if err != nil {
		logging.Error(fmt.Sprintf("Initial git mirror error: %v", err), "error", err.Error())
	}

	c.Start()
	logging.Info("Git mirror scheduler started. Waiting for signals...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logging.Info("Shutting down...")
	ctx := c.Stop()
	<-ctx.Done()
	return nil
}
