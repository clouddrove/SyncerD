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
		Short: "Your lightweight Docker registry sync engine",
		Long: `SyncerD - Your lightweight Docker registry sync engine.

SyncerD synchronizes Docker images from Docker Hub to other container 
registries (ECR, ACR, GCR, GitHub Container Registry).
It can monitor for new versions and automatically sync them on a schedule.`,
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
