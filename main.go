package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/gitsync"
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
	var rootCmd = &cobra.Command{
		Use:   "syncerd",
		Short: "Your lightweight artifact sync engine",
		Long: `SyncerD - Your lightweight artifact sync engine.

SyncerD synchronizes Docker images from Docker Hub to other container
registries (ECR, ACR, GCR, GitHub Container Registry), and mirrors git
repositories across GitHub, GitLab, Bitbucket, Azure DevOps, and AWS
CodeCommit, in any direction.`,
		Version: fmt.Sprintf("%s (commit: %s)", version, commit),
	}

	var cfgFile string
	var runOnce bool
	var syncReportPath string

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./syncerd.yaml)")

	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync images according to configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				log.Println("Running sync once...")
				report, err := syncer.SyncAll(context.Background())
				writeSyncReport(syncReportPath, report)
				return err
			}

			// Run with cron scheduler
			if cfg.Schedule == "" {
				cfg.Schedule = "0 0 */21 * *" // Every 3 weeks (21 days)
			}

			log.Printf("Starting SyncerD with schedule: %s", cfg.Schedule)
			return runWithCron(cfg, syncer, syncReportPath)
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
					log.Println("Dry run: no repository will be created or pushed")
				}
				report, err := syncer.SyncAll(context.Background())
				writeGitReport(gitReportPath, report, gitDryRun)
				return err
			}

			log.Printf("Starting SyncerD git-sync with schedule: %s", cfg.Git.Schedule)
			return runGitCron(cfg, syncer, gitReportPath)
		},
	}

	gitSyncCmd.Flags().BoolVar(&gitOnce, "once", false, "run the mirror once and exit (default: false, runs continuously with cron)")
	gitSyncCmd.Flags().BoolVar(&gitDryRun, "dry-run", false, "report what would be created, pushed, and pruned without writing anything")
	gitSyncCmd.Flags().StringVar(&gitReportPath, "report", "", "write a machine readable JSON report of the run to this path")
	rootCmd.AddCommand(gitSyncCmd)

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

// writeSyncReport writes report as JSON to path, when path is set. A write
// failure is logged and otherwise ignored: the run itself already
// happened, and a report that could not be written must not turn a
// successful sync into a failed process exit.
func writeSyncReport(path string, report *sync.Report) {
	if path == "" || report == nil {
		return
	}
	// Image sync has no dry run mode today.
	rr := report.ToRunReport(runreport.NewRunID(), false)
	if err := runreport.Write(path, rr); err != nil {
		log.Printf("failed to write run report to %s: %v", path, err)
	}
}

// writeGitReport writes report as JSON to path, when path is set. A write
// failure is logged and otherwise ignored, the same way Slack failures are
// already best effort.
func writeGitReport(path string, report *gitsync.GitReport, dryRun bool) {
	if path == "" || report == nil {
		return
	}
	rr := report.ToRunReport(runreport.NewRunID(), dryRun)
	if err := runreport.Write(path, rr); err != nil {
		log.Printf("failed to write run report to %s: %v", path, err)
	}
}

func runWithCron(cfg *config.Config, syncer *sync.Syncer, reportPath string) error {
	c := cron.New(cron.WithLocation(time.UTC))

	// Run immediately on startup
	log.Println("Running initial sync...")
	report, err := syncer.SyncAll(context.Background())
	writeSyncReport(reportPath, report)
	if err != nil {
		log.Printf("Initial sync error: %v", err)
	}

	// Schedule periodic syncs. Each run gets its own report, overwriting
	// the file from the previous run, so it always describes the most
	// recent one.
	_, err = c.AddFunc(cfg.Schedule, func() {
		log.Println("Running scheduled sync...")
		report, err := syncer.SyncAll(context.Background())
		writeSyncReport(reportPath, report)
		if err != nil {
			log.Printf("Scheduled sync error: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("invalid cron schedule: %w", err)
	}

	c.Start()
	log.Println("Cron scheduler started. Waiting for signals...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	ctx := c.Stop()
	<-ctx.Done()
	return nil
}

func runGitCron(cfg *config.Config, syncer *gitsync.Syncer, reportPath string) error {
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
		log.Println("Running scheduled git mirror...")
		report, err := syncer.SyncAll(context.Background())
		writeGitReport(reportPath, report, false)
		if err != nil {
			log.Printf("Scheduled git mirror error: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("invalid git cron schedule: %w", err)
	}

	log.Println("Running initial git mirror...")
	report, err := syncer.SyncAll(context.Background())
	writeGitReport(reportPath, report, false)
	if err != nil {
		log.Printf("Initial git mirror error: %v", err)
	}

	c.Start()
	log.Println("Git mirror scheduler started. Waiting for signals...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	ctx := c.Stop()
	<-ctx.Done()
	return nil
}
