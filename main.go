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
repositories between GitHub, GitLab, Bitbucket, Azure DevOps, and AWS
CodeCommit.`,
		Version: fmt.Sprintf("%s (commit: %s)", version, commit),
	}

	var cfgFile string
	var runOnce bool

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
				_, err := syncer.SyncAll(context.Background())
				return err
			}

			// Run with cron scheduler
			if cfg.Schedule == "" {
				cfg.Schedule = "0 0 */21 * *" // Every 3 weeks (21 days)
			}

			log.Printf("Starting SyncerD with schedule: %s", cfg.Schedule)
			return runWithCron(cfg, syncer)
		},
	}

	syncCmd.Flags().BoolVar(&runOnce, "once", false, "run sync once and exit (default: false, runs continuously with cron)")
	rootCmd.AddCommand(syncCmd)

	var gitOnce bool
	var gitDryRun bool

	gitSyncCmd := &cobra.Command{
		Use:   "git-sync",
		Short: "Mirror git repositories between hosting providers",
		Long: `Mirror git repositories between GitHub, GitLab, Bitbucket, Azure DevOps,
and AWS CodeCommit in any direction.

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
				_, err := syncer.SyncAll(context.Background())
				return err
			}

			log.Printf("Starting SyncerD git-sync with schedule: %s", cfg.Git.Schedule)
			return runGitCron(cfg, syncer)
		},
	}

	gitSyncCmd.Flags().BoolVar(&gitOnce, "once", false, "run the mirror once and exit (default: false, runs continuously with cron)")
	gitSyncCmd.Flags().BoolVar(&gitDryRun, "dry-run", false, "report what would be created, pushed, and pruned without writing anything")
	rootCmd.AddCommand(gitSyncCmd)

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runWithCron(cfg *config.Config, syncer *sync.Syncer) error {
	c := cron.New(cron.WithLocation(time.UTC))

	// Run immediately on startup
	log.Println("Running initial sync...")
	if _, err := syncer.SyncAll(context.Background()); err != nil {
		log.Printf("Initial sync error: %v", err)
	}

	// Schedule periodic syncs
	_, err := c.AddFunc(cfg.Schedule, func() {
		log.Println("Running scheduled sync...")
		if _, err := syncer.SyncAll(context.Background()); err != nil {
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

func runGitCron(cfg *config.Config, syncer *gitsync.Syncer) error {
	c := cron.New(cron.WithLocation(time.UTC))

	log.Println("Running initial git mirror...")
	if _, err := syncer.SyncAll(context.Background()); err != nil {
		log.Printf("Initial git mirror error: %v", err)
	}

	_, err := c.AddFunc(cfg.Git.Schedule, func() {
		log.Println("Running scheduled git mirror...")
		if _, err := syncer.SyncAll(context.Background()); err != nil {
			log.Printf("Scheduled git mirror error: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("invalid git cron schedule: %w", err)
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
