package sync

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/notify"
	"github.com/clouddrove/syncerd/internal/registry"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type SyncEvent struct {
	Destination string
	Image       string
	Tag         string
	Ref         string
}

type FailureEvent struct {
	Destination string
	Image       string
	Tag         string
	Ref         string
	Error       string
}

type Report struct {
	StartedAt time.Time
	EndedAt   time.Time
	NewSyncs  []SyncEvent
	Failures  []FailureEvent
}

type Syncer struct {
	config          *config.Config
	sourceRegistry  registry.Registry
	destRegistries  []registry.Registry
	factory         *registry.RegistryFactory
	keychain        authn.Keychain
	statePath       string
	state           *state.State
	currentReport   *Report
	slack           *notify.SlackClient
}

func NewSyncer(cfg *config.Config) (*Syncer, error) {
	factory := registry.NewRegistryFactory()

	// Create source registry
	sourceReg, err := factory.CreateSourceRegistry(
		cfg.Source.Type,
		cfg.Source.Registry,
		cfg.Source.Username,
		cfg.Source.Password,
		cfg.Source.Token,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create source registry: %w", err)
	}

	// Test source connection
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := factory.TestConnection(ctx, sourceReg); err != nil {
		return nil, fmt.Errorf("failed to authenticate with source registry: %w", err)
	}

	// Create destination registries
	var destRegs []registry.Registry
	for _, destCfg := range cfg.Destinations {
		destReg, err := factory.CreateDestinationRegistry(
			destCfg.Type,
			destCfg.Registry,
			destCfg.Region,
			destCfg.Auth,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create destination registry %s: %w", destCfg.Name, err)
		}

		destRegs = append(destRegs, destReg)
		log.Printf("Connected to destination registry: %s (%s)", destCfg.Name, destCfg.Type)
	}

	srcAuth := getSourceAuth(cfg)
	keychain := registry.SourceAwareKeychain{
		SourceAuth: srcAuth,
		Fallback:   authn.DefaultKeychain,
	}

	st, err := state.Load(cfg.StatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}

	var slackClient *notify.SlackClient
	if cfg.Slack.Enabled && cfg.Slack.WebhookURL != "" {
		slackClient = &notify.SlackClient{
			WebhookURL: cfg.Slack.WebhookURL,
			Channel:    cfg.Slack.Channel,
			Username:   cfg.Slack.Username,
			IconEmoji:  cfg.Slack.IconEmoji,
		}
	}

	return &Syncer{
		config:         cfg,
		sourceRegistry: sourceReg,
		destRegistries: destRegs,
		factory:        factory,
		keychain:       keychain,
		statePath:      cfg.StatePath,
		state:          st,
		slack:          slackClient,
	}, nil
}

func (s *Syncer) SyncAll(ctx context.Context) (*Report, error) {
	report := &Report{StartedAt: time.Now().UTC()}
	s.currentReport = report
	log.Println("Starting sync process...")

	for _, imgCfg := range s.config.Images {
		if err := s.SyncImage(ctx, imgCfg); err != nil {
			log.Printf("Error syncing image %s: %v", imgCfg.Name, err)
			if s.config.FailFast {
				report.EndedAt = time.Now().UTC()
				s.currentReport = nil
				_ = s.state.Save(s.statePath)
				// Slack best-effort
				if s.slack != nil && s.config.Slack.NotifyOnErr {
					_ = s.slack.SendText(ctx, s.formatSlackFailures(report.Failures))
				}
				return report, err
			}
		}
	}

	log.Println("Sync process completed")
	report.EndedAt = time.Now().UTC()
	s.currentReport = nil

	if err := s.state.Save(s.statePath); err != nil {
		return report, fmt.Errorf("failed to save state: %w", err)
	}

	// Slack notifications (best effort; do not fail the sync because Slack failed)
	if s.slack != nil {
		if s.config.Slack.NotifyOnNew && len(report.NewSyncs) > 0 {
			_ = s.slack.SendText(ctx, s.formatSlackNewSyncs(report.NewSyncs))
		}
		if s.config.Slack.NotifyOnErr && len(report.Failures) > 0 {
			_ = s.slack.SendText(ctx, s.formatSlackFailures(report.Failures))
		}
	}

	if len(report.Failures) > 0 {
		return report, fmt.Errorf("sync completed with %d failures", len(report.Failures))
	}
	return report, nil
}

func (s *Syncer) formatSlackNewSyncs(events []SyncEvent) string {
	if s != nil && s.config != nil && s.config.Slack.MessageFormat == "detailed" {
		return slackDetailedNewSyncs(events)
	}
	return slackCompactNewSyncs(events)
}

func (s *Syncer) formatSlackFailures(events []FailureEvent) string {
	if s != nil && s.config != nil && s.config.Slack.MessageFormat == "detailed" {
		return slackDetailedFailures(events)
	}
	return slackCompactFailures(events)
}

func slackCompactNewSyncs(events []SyncEvent) string {
	const max = 25
	msg := "*SyncerD*: new images/tags synced:\n"
	for i, e := range events {
		if i >= max {
			msg += fmt.Sprintf("…and %d more\n", len(events)-max)
			break
		}
		msg += fmt.Sprintf("- `%s`\n", e.Ref)
	}
	return msg
}

func slackCompactFailures(events []FailureEvent) string {
	const max = 25
	msg := "*SyncerD*: sync failures:\n"
	for i, e := range events {
		if i >= max {
			msg += fmt.Sprintf("…and %d more\n", len(events)-max)
			break
		}
		msg += fmt.Sprintf("- `%s` — %s\n", e.Ref, e.Error)
	}
	return msg
}

func slackDetailedNewSyncs(events []SyncEvent) string {
	const max = 75
	byDest := map[string][]SyncEvent{}
	for _, e := range events {
		byDest[e.Destination] = append(byDest[e.Destination], e)
	}
	msg := fmt.Sprintf("*SyncerD*: new images/tags synced (%d):\n", len(events))
	seen := 0
	for dest, list := range byDest {
		msg += fmt.Sprintf("\n*%s* (%d)\n", dest, len(list))
		for _, e := range list {
			seen++
			if seen > max {
				msg += fmt.Sprintf("…and %d more\n", len(events)-max)
				return msg
			}
			msg += fmt.Sprintf("- `%s`\n", e.Ref)
		}
	}
	return msg
}

func slackDetailedFailures(events []FailureEvent) string {
	const max = 75
	byDest := map[string][]FailureEvent{}
	for _, e := range events {
		byDest[e.Destination] = append(byDest[e.Destination], e)
	}
	msg := fmt.Sprintf("*SyncerD*: sync failures (%d):\n", len(events))
	seen := 0
	for dest, list := range byDest {
		msg += fmt.Sprintf("\n*%s* (%d)\n", dest, len(list))
		for _, e := range list {
			seen++
			if seen > max {
				msg += fmt.Sprintf("…and %d more\n", len(events)-max)
				return msg
			}
			msg += fmt.Sprintf("- `%s`\n  - %s\n", e.Ref, e.Error)
		}
	}
	return msg
}

func (s *Syncer) SyncImage(ctx context.Context, imgCfg config.ImageConfig) error {
	log.Printf("Syncing image: %s", imgCfg.Name)

	// Normalize image name
	imageName := registry.NormalizeDockerHubImage(imgCfg.Name)

	// Get tags to sync
	var tagsToSync []string
	if len(imgCfg.Tags) > 0 {
		// Sync specific tags
		tagsToSync = imgCfg.Tags
	} else if imgCfg.WatchTags {
		// Get all tags from source
		var tags []string
		err := retry(ctx, 3, 2*time.Second, func() error {
			var err error
			tags, err = s.sourceRegistry.ListTags(ctx, imageName)
			return err
		})
		if err != nil {
			if s.currentReport != nil {
				s.currentReport.Failures = append(s.currentReport.Failures, FailureEvent{
					Destination: "source",
					Image:       imageName,
					Tag:         "*",
					Ref:         fmt.Sprintf("%s/%s:*", s.sourceRegistry.GetRegistryURL(), imageName),
					Error:       fmt.Sprintf("list tags: %v", err),
				})
			}
			return fmt.Errorf("failed to list tags: %w", err)
		}
		// Filter to only tags that are not yet synced to at least one destination
		for _, tag := range tags {
			needsSync := false
			for _, dest := range s.config.Destinations {
				if !s.state.IsSynced(dest.Name, imageName, tag) {
					needsSync = true
					break
				}
			}
			if needsSync {
				tagsToSync = append(tagsToSync, tag)
			}
		}
		log.Printf("Found %d tags for %s", len(tagsToSync), imageName)
	} else {
		// Default: sync latest tag
		tagsToSync = []string{"latest"}
	}

	// Sync each tag to all destinations
	for _, tag := range tagsToSync {
		if err := s.SyncTag(ctx, imageName, tag, imgCfg); err != nil {
			log.Printf("Error syncing %s:%s: %v", imageName, tag, err)
			if s.config.FailFast {
				return err
			}
		}
	}

	return nil
}

func (s *Syncer) SyncTag(ctx context.Context, imageName, tag string, imgCfg config.ImageConfig) error {
	sourceRef := fmt.Sprintf("%s/%s:%s", s.sourceRegistry.GetRegistryURL(), imageName, tag)
	log.Printf("Syncing tag: %s", sourceRef)

	// Copy image to each destination
	for i, destReg := range s.destRegistries {
		destCfg := s.config.Destinations[i]
		destImageName := s.getDestinationImageName(imageName, destCfg)
		destRef := fmt.Sprintf("%s/%s:%s", destReg.GetRegistryURL(), destImageName, tag)

		if s.state.IsSynced(destCfg.Name, imageName, tag) {
			log.Printf("Already synced (state): %s -> %s", sourceRef, destRef)
			continue
		}

		// Check if image already exists in destination
		exists, err := destReg.ImageExists(ctx, destImageName, tag)
		if err != nil {
			log.Printf("Warning: failed to check if image exists in %s: %v", destCfg.Name, err)
		}

		if exists {
			log.Printf("Image %s:%s already exists in %s, skipping", destImageName, tag, destCfg.Name)
			s.state.MarkSynced(destCfg.Name, imageName, tag)
			continue
		}

		// Copy image using crane
		if err := s.copyImage(ctx, sourceRef, destReg, destImageName, tag); err != nil {
			log.Printf("Failed to copy to %s: %v", destCfg.Name, err)
			if s.currentReport != nil {
				s.currentReport.Failures = append(s.currentReport.Failures, FailureEvent{
					Destination: destCfg.Name,
					Image:       imageName,
					Tag:         tag,
					Ref:         destRef,
					Error:       err.Error(),
				})
			}
			if s.config.FailFast {
				return err
			}
			continue
		}

		s.state.MarkSynced(destCfg.Name, imageName, tag)
		if s.currentReport != nil {
			s.currentReport.NewSyncs = append(s.currentReport.NewSyncs, SyncEvent{
				Destination: destCfg.Name,
				Image:       imageName,
				Tag:         tag,
				Ref:         destRef,
			})
		}
		log.Printf("Successfully synced %s:%s to %s (%s)", destImageName, tag, destCfg.Name, destCfg.Type)
	}

	return nil
}

func (s *Syncer) copyImage(ctx context.Context, sourceRef string, destReg registry.Registry, destImage, destTag string) error {
	destRef := fmt.Sprintf("%s/%s:%s", destReg.GetRegistryURL(), destImage, destTag)

	// Copy image using crane, sourcing auth from config (Docker Hub) and destination
	// auth from docker credential config (authn.DefaultKeychain).
	if err := retry(ctx, 3, 3*time.Second, func() error {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		return crane.Copy(
			sourceRef,
			destRef,
			crane.WithContext(opCtx),
			crane.WithAuthFromKeychain(s.keychain),
		)
	}); err != nil {
		return fmt.Errorf("failed to copy image: %w", err)
	}

	return nil
}

func (s *Syncer) getDestinationImageName(sourceImageName string, destCfg config.DestinationConfig) string {
	// For now, use the same image name
	// Could be customized per destination if needed
	switch destCfg.Type {
	case "gcr":
		// GCR might need project prefix, but it's usually in the registry URL
		return sourceImageName
	case "acr":
		// ACR uses the same format
		return sourceImageName
	case "ecr":
		// ECR uses the same format
		return sourceImageName
	case "ghcr":
		// GHCR might need owner prefix
		return sourceImageName
	default:
		return sourceImageName
	}
}

func getSourceAuth(cfg *config.Config) authn.Authenticator {
	if cfg.Source.Token != "" {
		return &authn.Bearer{Token: cfg.Source.Token}
	}
	if cfg.Source.Username != "" && cfg.Source.Password != "" {
		return &authn.Basic{
			Username: cfg.Source.Username,
			Password: cfg.Source.Password,
		}
	}
	return authn.Anonymous
}

func retry(ctx context.Context, attempts int, baseDelay time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		// backoff with jitter
		if i < attempts-1 {
			jitter := time.Duration(rand.Int63n(int64(baseDelay / 2)))
			sleep := baseDelay*time.Duration(i+1) + jitter
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}
