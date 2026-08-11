package gitsync

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/notify"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
	"github.com/clouddrove/syncerd/internal/vcs/github"
	"github.com/clouddrove/syncerd/internal/vcs/gitlab"
)

// newRegistry returns a registry with every implemented provider type.
// Types that validate but have no implementation yet are absent, so
// BuildMirrors reports them clearly rather than failing at run time.
func newRegistry() *vcs.Registry {
	r := vcs.NewRegistry()

	r.Register("github", func(c vcs.ProviderConfig) (vcs.Provider, error) {
		return github.New(github.Config{
			Name: c.Name, Owner: c.Owner, APIURL: c.APIURL, Token: c.Token,
		})
	})
	r.Register("gitlab", func(c vcs.ProviderConfig) (vcs.Provider, error) {
		return gitlab.New(gitlab.Config{
			Name: c.Name, Owner: c.Owner, APIURL: c.APIURL, Token: c.Token,
		})
	})

	return r
}

// toProviderConfig converts configuration into the vcs construction input.
func toProviderConfig(p config.GitProviderConfig) vcs.ProviderConfig {
	return vcs.ProviderConfig{
		Name: p.Name, Type: p.Type, Owner: p.Owner, APIURL: p.APIURL,
		Token: p.Token, Email: p.Email, Project: p.Project, Auth: p.Auth,
		Region: p.Region, GitUsername: p.GitUsername, GitPassword: p.GitPassword,
	}
}

// BuildMirrors constructs every provider once and resolves each mirror pair
// into a runnable Mirror. It also returns a redactor primed with every
// configured secret.
func BuildMirrors(cfg *config.GitConfig) ([]Mirror, *Redactor, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("git configuration is required")
	}

	registry := newRegistry()
	redactor := NewRedactor(cfg.Secrets()...)

	built := make(map[string]vcs.Provider, len(cfg.Providers))
	for _, pc := range cfg.Providers {
		p, err := registry.Build(toProviderConfig(pc))
		if err != nil {
			return nil, nil, err
		}
		built[pc.Name] = p
	}

	mirrors := make([]Mirror, 0, len(cfg.Mirrors))
	for _, mc := range cfg.Mirrors {
		src, ok := built[mc.Source]
		if !ok {
			return nil, nil, fmt.Errorf("mirror %q: unknown source provider %q", mc.Name, mc.Source)
		}
		dst, ok := built[mc.Destination]
		if !ok {
			return nil, nil, fmt.Errorf("mirror %q: unknown destination provider %q", mc.Name, mc.Destination)
		}

		lister, ok := src.(vcs.Lister)
		if !ok {
			return nil, nil, fmt.Errorf("mirror %q: source provider %q cannot list repositories", mc.Name, mc.Source)
		}

		var ensurer vcs.Ensurer
		if mc.CreateMissingOrDefault() {
			ensurer, ok = dst.(vcs.Ensurer)
			if !ok {
				return nil, nil, fmt.Errorf("mirror %q: destination provider %q cannot create repositories; set create_missing: false", mc.Name, mc.Destination)
			}
		}

		names, err := vcs.ParseNameTemplate(mc.NameTemplate)
		if err != nil {
			return nil, nil, fmt.Errorf("mirror %q: %w", mc.Name, err)
		}
		if names.ProducesNestedName() && !dst.SupportsNesting() {
			return nil, nil, fmt.Errorf("mirror %q: name_template renders a nested name but destination %q does not support nested paths", mc.Name, mc.Destination)
		}

		mode, err := ParsePushMode(mc.PushMode)
		if err != nil {
			return nil, nil, fmt.Errorf("mirror %q: %w", mc.Name, err)
		}

		mirrors = append(mirrors, Mirror{
			Name:         mc.Name,
			Source:       lister,
			SourceRemote: src,
			Dest:         dst,
			DestEnsurer:  ensurer,
			Filter: vcs.Filter{
				Include:      mc.Filters.Include,
				Exclude:      mc.Filters.Exclude,
				SkipArchived: mc.Filters.SkipArchivedOrDefault(),
				SkipForks:    mc.Filters.SkipForksOrDefault(),
			},
			Names:         names,
			PushMode:      mode,
			Adopt:         mc.Adopt,
			CreateMissing: mc.CreateMissingOrDefault(),
			Visibility:    mc.Visibility,
		})
	}

	return mirrors, redactor, nil
}

// Syncer runs git mirroring for a loaded configuration and reports to Slack.
type Syncer struct {
	cfg     *config.Config
	engine  *Engine
	mirrors []Mirror
	slack   *notify.SlackClient
}

// NewSyncer builds every provider and prepares the engine.
func NewSyncer(cfg *config.Config, dryRun bool) (*Syncer, error) {
	mirrors, redactor, err := BuildMirrors(cfg.Git)
	if err != nil {
		return nil, err
	}

	st, err := state.LoadGit(cfg.Git.StatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load git state: %w", err)
	}

	engine := NewEngine(EngineOptions{
		WorkDir:     cfg.Git.WorkDir,
		Concurrency: cfg.Git.Concurrency,
		DryRun:      dryRun,
		FailFast:    cfg.FailFast,
		State:       st,
		StatePath:   cfg.Git.StatePath,
		Runner:      NewRunner(redactor),
		Redactor:    redactor,
	})

	var slackClient *notify.SlackClient
	if cfg.Slack.Enabled && cfg.Slack.WebhookURL != "" {
		slackClient = &notify.SlackClient{
			WebhookURL: cfg.Slack.WebhookURL,
			Channel:    cfg.Slack.Channel,
			Username:   cfg.Slack.Username,
			IconEmoji:  cfg.Slack.IconEmoji,
		}
	}

	return &Syncer{cfg: cfg, engine: engine, mirrors: mirrors, slack: slackClient}, nil
}

// SyncAll runs every mirror and sends Slack notifications, best effort.
func (s *Syncer) SyncAll(ctx context.Context) (*GitReport, error) {
	log.Println("Starting git mirror run...")
	rep, runErr := s.engine.Run(ctx, s.mirrors)
	log.Printf("Git mirror run finished in %s: %d mirrored, %d skipped, %d failures",
		rep.EndedAt.Sub(rep.StartedAt).Round(time.Second),
		len(rep.Mirrored), rep.Skipped, len(rep.Failures))

	if s.slack != nil {
		detailed := s.cfg.Slack.MessageFormat == "detailed"
		if s.cfg.Slack.NotifyOnNew && len(rep.Mirrored) > 0 {
			_ = s.slack.Send(ctx, BuildMirroredMessage(rep, detailed))
		}
		if s.cfg.Slack.NotifyOnErr && len(rep.Failures) > 0 {
			_ = s.slack.Send(ctx, BuildFailureMessage(rep, detailed))
		}
	}

	return rep, runErr
}
