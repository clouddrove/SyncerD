package sync

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/logging"
	"github.com/clouddrove/syncerd/internal/notify"
	"github.com/clouddrove/syncerd/internal/registry"
	"github.com/clouddrove/syncerd/internal/runreport"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
)

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
	Skipped   int
	Failures  []FailureEvent
}

// ToRunReport maps a Report onto the schema shared with git-sync, so a
// consumer of --report does not need to know which command produced the
// file. Image sync has no dry run mode today; callers should pass false
// for dryRun. Success is true only when nothing failed.
func (r *Report) ToRunReport(runID string, dryRun bool) runreport.Report {
	items := make([]runreport.Item, 0, len(r.NewSyncs))
	for _, ev := range r.NewSyncs {
		items = append(items, runreport.Item{
			Group:       ev.Destination,
			Source:      fmt.Sprintf("%s:%s", ev.Image, ev.Tag),
			Destination: ev.Ref,
		})
	}

	failures := make([]runreport.Failure, 0, len(r.Failures))
	for _, f := range r.Failures {
		failures = append(failures, runreport.Failure{
			Group:       f.Destination,
			Source:      fmt.Sprintf("%s:%s", f.Image, f.Tag),
			Destination: f.Ref,
			Stage:       "",
			Error:       f.Error,
		})
	}

	// StartedAt is set with time.Now().UTC(), and .UTC() strips the
	// monotonic clock reading that Sub would otherwise use, so this
	// subtraction is pure wall clock. A backward NTP step during a long run
	// can make it negative; clamp to zero so this agrees with the metrics
	// writer, which already clamps the same way.
	duration := r.EndedAt.Sub(r.StartedAt).Seconds()
	if duration < 0 {
		duration = 0
	}

	return runreport.Report{
		SchemaVersion: runreport.SchemaVersion,
		RunID:         runID,
		Command:       "sync",
		StartedAt:     r.StartedAt,
		EndedAt:       r.EndedAt,
		DurationSecs:  duration,
		Success:       len(r.Failures) == 0,
		DryRun:        dryRun,
		Counts: runreport.Counts{
			Succeeded: len(r.NewSyncs),
			Skipped:   r.Skipped,
			Failed:    len(r.Failures),
		},
		Items:    items,
		Failures: failures,
	}
}

type Syncer struct {
	config         *config.Config
	sourceRegistry registry.Registry
	destRegistries []registry.Registry
	factory        *registry.RegistryFactory
	keychain       authn.Keychain
	statePath      string
	state          *state.State
	currentReport  *Report
	slack          *notify.SlackClient
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
		logging.Info(fmt.Sprintf("Connected to destination registry: %s (%s)", destCfg.Name, destCfg.Type))
	}

	srcAuth := getSourceAuth(cfg)
	keychain := registry.SourceAwareKeychain{
		SourceAuth: srcAuth,
		Fallback:   registry.DefaultDestinationKeychain(),
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
	logging.Info("Starting sync process...")

	for _, imgCfg := range s.config.Images {
		if err := s.SyncImage(ctx, imgCfg); err != nil {
			logging.Error(fmt.Sprintf("Error syncing image %s: %v", imgCfg.Name, err),
				"image", imgCfg.Name, "error", err.Error())
			if s.config.FailFast {
				report.EndedAt = time.Now().UTC()
				s.currentReport = nil
				_ = s.state.Save(s.statePath)
				// Slack best-effort
				if s.slack != nil && s.config.Slack.NotifyOnErr {
					_ = s.slack.Send(ctx, s.buildFailureMessage(report.Failures))
				}
				return report, err
			}
		}
	}

	logging.Info("Sync process completed",
		"synced", len(report.NewSyncs), "skipped", report.Skipped, "failed", len(report.Failures))
	report.EndedAt = time.Now().UTC()
	s.currentReport = nil

	if err := s.state.Save(s.statePath); err != nil {
		return report, fmt.Errorf("failed to save state: %w", err)
	}

	// Slack notifications (best effort; do not fail the sync because Slack failed)
	if s.slack != nil {
		if s.config.Slack.NotifyOnNew && len(report.NewSyncs) > 0 {
			_ = s.slack.Send(ctx, s.buildNewSyncsMessage(report.NewSyncs))
		}
		if s.config.Slack.NotifyOnErr && len(report.Failures) > 0 {
			_ = s.slack.Send(ctx, s.buildFailureMessage(report.Failures))
		}
	}

	if len(report.Failures) > 0 {
		return report, fmt.Errorf("sync completed with %d failures", len(report.Failures))
	}
	return report, nil
}

// detailed reports whether notifications should be grouped per destination.
func (s *Syncer) detailed() bool {
	return s != nil && s.config != nil && s.config.Slack.MessageFormat == "detailed"
}

func (s *Syncer) buildNewSyncsMessage(events []SyncEvent) notify.Message {
	const max = 50
	n := len(events)
	m := notify.Message{
		Emoji:    ":white_check_mark:",
		Title:    fmt.Sprintf("%d image(s)/tag(s) synced", n),
		Color:    notify.ColorSuccess,
		Fallback: fmt.Sprintf("SyncerD: %d image(s)/tag(s) synced", n),
	}

	if s.detailed() {
		byDest := map[string][]SyncEvent{}
		order := []string{}
		for _, e := range events {
			if _, ok := byDest[e.Destination]; !ok {
				order = append(order, e.Destination)
			}
			byDest[e.Destination] = append(byDest[e.Destination], e)
		}
		seen := 0
		for _, dest := range order {
			list := byDest[dest]
			sec := notify.Section{Heading: fmt.Sprintf("%s (%d)", dest, len(list))}
			for _, e := range list {
				if seen >= max {
					sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", n-max))
					m.Sections = append(m.Sections, sec)
					return m
				}
				sec.Lines = append(sec.Lines, "• `"+e.Ref+"`")
				seen++
			}
			m.Sections = append(m.Sections, sec)
		}
		return m
	}

	sec := notify.Section{}
	for i, e := range events {
		if i >= max {
			sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", n-max))
			break
		}
		sec.Lines = append(sec.Lines, "• `"+e.Ref+"`")
	}
	m.Sections = []notify.Section{sec}
	return m
}

func (s *Syncer) buildFailureMessage(events []FailureEvent) notify.Message {
	const max = 50
	n := len(events)
	m := notify.Message{
		Emoji:    ":rotating_light:",
		Title:    fmt.Sprintf("%d sync failure(s)", n),
		Color:    notify.ColorFailure,
		Fallback: fmt.Sprintf("SyncerD: %d sync failure(s)", n),
	}

	if s.detailed() {
		byDest := map[string][]FailureEvent{}
		order := []string{}
		for _, e := range events {
			if _, ok := byDest[e.Destination]; !ok {
				order = append(order, e.Destination)
			}
			byDest[e.Destination] = append(byDest[e.Destination], e)
		}
		seen := 0
		for _, dest := range order {
			list := byDest[dest]
			sec := notify.Section{Heading: fmt.Sprintf("%s (%d)", dest, len(list))}
			for _, e := range list {
				if seen >= max {
					sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", n-max))
					m.Sections = append(m.Sections, sec)
					return m
				}
				sec.Lines = append(sec.Lines, fmt.Sprintf("• `%s`\n  ↳ _%s_", e.Ref, e.Error))
				seen++
			}
			m.Sections = append(m.Sections, sec)
		}
		return m
	}

	sec := notify.Section{}
	for i, e := range events {
		if i >= max {
			sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", n-max))
			break
		}
		sec.Lines = append(sec.Lines, fmt.Sprintf("• `%s`\n  ↳ _%s_", e.Ref, e.Error))
	}
	m.Sections = []notify.Section{sec}
	return m
}

func (s *Syncer) SyncImage(ctx context.Context, imgCfg config.ImageConfig) error {
	logging.Info(fmt.Sprintf("Syncing image: %s", imgCfg.Name))

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
		logging.Info(fmt.Sprintf("Found %d tags for %s", len(tagsToSync), imageName))
	} else {
		// Default: sync latest tag
		tagsToSync = []string{"latest"}
	}

	// Sync each tag to all destinations
	for _, tag := range tagsToSync {
		if err := s.SyncTag(ctx, imageName, tag, imgCfg); err != nil {
			logging.Error(fmt.Sprintf("Error syncing %s:%s: %v", imageName, tag, err),
				"image", imageName, "tag", tag, "error", err.Error())
			if s.config.FailFast {
				return err
			}
		}
	}

	return nil
}

func (s *Syncer) SyncTag(ctx context.Context, imageName, tag string, imgCfg config.ImageConfig) error {
	sourceRef := fmt.Sprintf("%s/%s:%s", s.sourceRegistry.GetRegistryURL(), imageName, tag)
	logging.Info(fmt.Sprintf("Syncing tag: %s", sourceRef))

	// Copy image to each destination
	for i, destReg := range s.destRegistries {
		destCfg := s.config.Destinations[i]
		destImageName := imageName
		destRef := fmt.Sprintf("%s/%s:%s", destReg.GetRegistryURL(), destImageName, tag)

		if s.state.IsSynced(destCfg.Name, imageName, tag) {
			logging.Info(fmt.Sprintf("Already synced (state): %s -> %s", sourceRef, destRef))
			if s.currentReport != nil {
				s.currentReport.Skipped++
			}
			continue
		}

		// Check if image already exists in destination
		exists, err := destReg.ImageExists(ctx, destImageName, tag)
		if err != nil {
			logging.Warn(fmt.Sprintf("Warning: failed to check if image exists in %s: %v", destCfg.Name, err),
				"destination", destCfg.Name, "error", err.Error())
		}

		if exists {
			logging.Info(fmt.Sprintf("Image %s:%s already exists in %s, skipping", destImageName, tag, destCfg.Name))
			s.state.MarkSynced(destCfg.Name, imageName, tag)
			if s.currentReport != nil {
				s.currentReport.Skipped++
			}
			continue
		}

		// Copy image using crane
		if err := s.copyImage(ctx, sourceRef, destReg, destImageName, tag); err != nil {
			logging.Error(fmt.Sprintf("Failed to copy to %s: %v", destCfg.Name, err),
				"destination", destCfg.Name, "image", imageName, "tag", tag, "error", err.Error())
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
		logging.Info(fmt.Sprintf("Successfully synced %s:%s to %s (%s)", destImageName, tag, destCfg.Name, destCfg.Type),
			"image", destImageName, "tag", tag, "destination", destCfg.Name, "destination_type", destCfg.Type)
	}

	return nil
}

func (s *Syncer) copyImage(ctx context.Context, sourceRef string, destReg registry.Registry, destImage, destTag string) error {
	destRef := fmt.Sprintf("%s/%s:%s", destReg.GetRegistryURL(), destImage, destTag)

	// Copy image using crane, sourcing auth from config (Docker Hub) and destination
	// auth from docker credential config, or from the AWS credential chain for
	// private ECR hosts.
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
