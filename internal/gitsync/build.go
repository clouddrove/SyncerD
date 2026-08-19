package gitsync

import (
	"context"
	"fmt"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/logging"
	"github.com/clouddrove/syncerd/internal/notify"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
	"github.com/clouddrove/syncerd/internal/vcs/azuredevops"
	"github.com/clouddrove/syncerd/internal/vcs/bitbucket"
	"github.com/clouddrove/syncerd/internal/vcs/codecommit"
	"github.com/clouddrove/syncerd/internal/vcs/github"
	"github.com/clouddrove/syncerd/internal/vcs/gitlab"
)

// newRegistry returns a registry with every implemented provider type:
// github, gitlab, bitbucket, azuredevops, and codecommit. The registration
// mechanism stays in place for whatever provider type comes next: a type
// with no entry here is reported clearly by BuildMirrors rather than
// failing at run time.
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
	r.Register("bitbucket", func(c vcs.ProviderConfig) (vcs.Provider, error) {
		return bitbucket.New(bitbucket.Config{
			Name: c.Name, Owner: c.Owner, APIURL: c.APIURL, Email: c.Email, Token: c.Token,
		})
	})
	r.Register("azuredevops", func(c vcs.ProviderConfig) (vcs.Provider, error) {
		return azuredevops.New(azuredevops.Config{
			Name: c.Name, Owner: c.Owner, Project: c.Project, APIURL: c.APIURL, Auth: c.Auth, Token: c.Token,
		})
	})
	r.Register("codecommit", func(c vcs.ProviderConfig) (vcs.Provider, error) {
		return codecommit.New(codecommit.Config{
			Name: c.Name, Region: c.Region, GitUsername: c.GitUsername, GitPassword: c.GitPassword,
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
	// The redactor is primed from every configured secret, not only the
	// ones referenced by a mirror below, since an unreferenced secret in
	// the config is still a secret worth redacting if it somehow appears
	// in output.
	redactor := NewRedactor(cfg.Secrets()...)

	// Only construct providers a mirror actually references. A config that
	// declares a provider for later use, or as a spare, must not block
	// startup just because that provider's type has no implementation yet.
	referenced := make(map[string]bool, len(cfg.Mirrors)*2)
	for _, mc := range cfg.Mirrors {
		referenced[mc.Source] = true
		referenced[mc.Destination] = true
	}

	built := make(map[string]vcs.Provider, len(referenced))
	for _, pc := range cfg.Providers {
		if !referenced[pc.Name] {
			continue
		}
		p, err := registry.Build(toProviderConfig(pc))
		if err != nil {
			return nil, nil, fmt.Errorf("git.providers %q: %w", pc.Name, err)
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

		// A source that cannot list pull requests is refused rather than
		// quietly mirroring branches only: the operator asked for pull
		// request heads, and a run that silently omits them looks like a
		// working mirror.
		var sourcePRs vcs.PullRequestLister
		if mc.PullRequests.Enabled {
			sourcePRs, ok = src.(vcs.PullRequestLister)
			if !ok {
				return nil, nil, fmt.Errorf("mirror %q: pull_requests is enabled but source provider %q (type %q) cannot list pull requests", mc.Name, mc.Source, src.Type())
			}
		}

		// Mirroring the objects needs a destination that can write them,
		// and, when the conversation is included, both sides able to read
		// and write comments. Refusing here beats a run that quietly
		// mirrors branches and calls it done.
		var destPRs vcs.PullRequestWriter
		var sourceConv, destConv vcs.PullRequestConversation
		if mc.PullRequests.MirrorObjects {
			destPRs, ok = dst.(vcs.PullRequestWriter)
			if !ok {
				return nil, nil, fmt.Errorf("mirror %q: pull_requests.mirror_objects is set but destination provider %q (type %q) cannot write pull requests", mc.Name, mc.Destination, dst.Type())
			}
			if mc.PullRequests.CommentsOrDefault() || mc.PullRequests.ReviewsOrDefault() {
				sourceConv, ok = src.(vcs.PullRequestConversation)
				if !ok {
					return nil, nil, fmt.Errorf("mirror %q: pull request comments are enabled but source provider %q (type %q) cannot read them; set comments: false and reviews: false", mc.Name, mc.Source, src.Type())
				}
				destConv, ok = dst.(vcs.PullRequestConversation)
				if !ok {
					return nil, nil, fmt.Errorf("mirror %q: pull request comments are enabled but destination provider %q (type %q) cannot write them; set comments: false and reviews: false", mc.Name, mc.Destination, dst.Type())
				}
			}
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
			SourcePRs:     sourcePRs,
			DestPRs:       destPRs,
			SourceConv:    sourceConv,
			DestConv:      destConv,
			PullRequests: PRSyncConfig{
				Enabled:       mc.PullRequests.Enabled,
				BranchPrefix:  mc.PullRequests.BranchPrefixOrDefault(),
				MirrorObjects: mc.PullRequests.MirrorObjects,
				Comments:      mc.PullRequests.CommentsOrDefault(),
				Reviews:       mc.PullRequests.ReviewsOrDefault(),
				Labels:        mc.PullRequests.LabelsOrDefault(),
			},
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
	dryRun  bool
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

	return &Syncer{cfg: cfg, engine: engine, mirrors: mirrors, slack: slackClient, dryRun: dryRun}, nil
}

// SyncAll runs every mirror and sends Slack notifications, best effort.
func (s *Syncer) SyncAll(ctx context.Context) (*GitReport, error) {
	logging.Info("Starting git mirror run...")
	rep, runErr := s.engine.Run(ctx, s.mirrors)
	duration := rep.EndedAt.Sub(rep.StartedAt).Round(time.Second)
	logging.Info(fmt.Sprintf("Git mirror run finished in %s: %d mirrored, %d skipped, %d failures",
		duration, len(rep.Mirrored), rep.Skipped, len(rep.Failures)),
		"duration", duration.String(), "mirrored", len(rep.Mirrored), "skipped", rep.Skipped, "failed", len(rep.Failures))

	// A dry run reports to the operator through the log, never to Slack.
	// Its report describes what would happen, and posting that as a
	// success would be a lie.
	if s.slack != nil && !s.dryRun {
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
