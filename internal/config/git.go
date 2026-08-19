package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// Supported provider types. Only github and gitlab are implemented in this
// plan; the rest are accepted by validation so their configs can be written
// ahead of the provider packages landing.
var supportedProviderTypes = map[string]bool{
	"github":      true,
	"gitlab":      true,
	"bitbucket":   true,
	"azuredevops": true,
	"codecommit":  true,
}

// flatProviderTypes cannot represent a nested repository path.
var flatProviderTypes = map[string]bool{
	"azuredevops": true,
	"codecommit":  true,
}

var validPushModes = map[string]bool{
	"mirror":       true,
	"additive":     true,
	"fast-forward": true,
}

var validVisibilities = map[string]bool{
	"private": true,
	"public":  true,
}

// GitConfig is the top level git mirroring configuration.
type GitConfig struct {
	Providers   []GitProviderConfig `mapstructure:"providers"`
	Mirrors     []MirrorConfig      `mapstructure:"mirrors"`
	WorkDir     string              `mapstructure:"work_dir"`
	StatePath   string              `mapstructure:"state_path"`
	Schedule    string              `mapstructure:"schedule"`
	Concurrency int                 `mapstructure:"concurrency"`
}

// GitProviderConfig describes one git hosting provider. Fields are a union
// across provider types; validation enforces the per type requirements.
type GitProviderConfig struct {
	Name   string `mapstructure:"name"`
	Type   string `mapstructure:"type"`
	Owner  string `mapstructure:"owner"`
	APIURL string `mapstructure:"api_url"`
	Token  string `mapstructure:"token"`

	Email string `mapstructure:"email"` // bitbucket

	Project string `mapstructure:"project"` // azuredevops
	Auth    string `mapstructure:"auth"`    // azuredevops: pat | entra

	Region      string `mapstructure:"region"`       // codecommit
	GitUsername string `mapstructure:"git_username"` // codecommit: IAM HTTPS Git credentials, required
	GitPassword string `mapstructure:"git_password"` // codecommit: IAM HTTPS Git credentials, required
}

// FilterConfig selects which discovered repositories to mirror. The bool
// pointers distinguish "unset" from an explicit false.
type FilterConfig struct {
	Include      []string `mapstructure:"include"`
	Exclude      []string `mapstructure:"exclude"`
	SkipArchived *bool    `mapstructure:"skip_archived"`
	SkipForks    *bool    `mapstructure:"skip_forks"`
}

// SkipArchivedOrDefault reports the effective skip_archived value.
func (f FilterConfig) SkipArchivedOrDefault() bool {
	if f.SkipArchived == nil {
		return true
	}
	return *f.SkipArchived
}

// SkipForksOrDefault reports the effective skip_forks value.
func (f FilterConfig) SkipForksOrDefault() bool {
	if f.SkipForks == nil {
		return true
	}
	return *f.SkipForks
}

// MirrorConfig is one ordered source to destination pair.
type MirrorConfig struct {
	Name          string       `mapstructure:"name"`
	Source        string       `mapstructure:"source"`
	Destination   string       `mapstructure:"destination"`
	Filters       FilterConfig `mapstructure:"filters"`
	CreateMissing *bool        `mapstructure:"create_missing"`
	Visibility    string       `mapstructure:"visibility"`
	PushMode      string       `mapstructure:"push_mode"`
	Adopt         bool         `mapstructure:"adopt"`
	NameTemplate  string       `mapstructure:"name_template"`

	PullRequests PullRequestsConfig `mapstructure:"pull_requests"`
}

// CreateMissingOrDefault reports the effective create_missing value.
func (m MirrorConfig) CreateMissingOrDefault() bool {
	if m.CreateMissing == nil {
		return true
	}
	return *m.CreateMissing
}

// defaultPRBranchPrefix is the branch namespace pull request heads are
// mirrored into when a mirror does not name one.
const defaultPRBranchPrefix = "syncerd/pr"

// PullRequestsConfig enables pull request mirroring for one mirror. It is
// disabled by default: an enabled mirror pushes the head of every open
// fork pull request, which is third party code, into destination branches.
type PullRequestsConfig struct {
	Enabled      bool     `mapstructure:"enabled"`
	BranchPrefix string   `mapstructure:"branch_prefix"`
	States       []string `mapstructure:"states"`

	// MirrorObjects recreates each source pull request as a real pull
	// request at the destination. It also makes every mirrored pull
	// request get a branch under BranchPrefix, including one whose head
	// lives in the source repository, so a destination pull request always
	// has one uniform head name to point at.
	MirrorObjects bool `mapstructure:"mirror_objects"`

	// The bool pointers distinguish unset from an explicit false, so the
	// defaults below apply only where the operator said nothing.
	Comments *bool `mapstructure:"comments"`
	Reviews  *bool `mapstructure:"reviews"`
	Labels   *bool `mapstructure:"labels"`
}

// CommentsOrDefault reports whether discussion and review comments are
// mirrored. On by default once objects are mirrored: a pull request with no
// conversation is a poor mirror of one that has a conversation.
func (p PullRequestsConfig) CommentsOrDefault() bool {
	if p.Comments == nil {
		return p.MirrorObjects
	}
	return *p.Comments
}

// ReviewsOrDefault reports whether review verdicts are mirrored as text.
func (p PullRequestsConfig) ReviewsOrDefault() bool {
	if p.Reviews == nil {
		return p.MirrorObjects
	}
	return *p.Reviews
}

// LabelsOrDefault reports whether labels are applied at the destination.
func (p PullRequestsConfig) LabelsOrDefault() bool {
	if p.Labels == nil {
		return true
	}
	return *p.Labels
}

// BranchPrefixOrDefault reports the effective branch prefix.
func (p PullRequestsConfig) BranchPrefixOrDefault() string {
	if p.BranchPrefix == "" {
		return defaultPRBranchPrefix
	}
	return p.BranchPrefix
}

// Provider looks up a provider by name.
func (g *GitConfig) Provider(name string) (*GitProviderConfig, bool) {
	if g == nil {
		return nil, false
	}
	for i := range g.Providers {
		if g.Providers[i].Name == name {
			return &g.Providers[i], true
		}
	}
	return nil, false
}

// Secrets returns every configured secret, for the output redactor.
func (g *GitConfig) Secrets() []string {
	if g == nil {
		return nil
	}
	var out []string
	for _, p := range g.Providers {
		if p.Token != "" {
			out = append(out, p.Token)
		}
		if p.GitPassword != "" {
			out = append(out, p.GitPassword)
		}
	}
	return out
}

// ApplyDefaults fills unset values. Safe to call more than once.
func (g *GitConfig) ApplyDefaults() {
	if g == nil {
		return
	}
	if g.WorkDir == "" {
		g.WorkDir = "/var/lib/syncerd/git"
	}
	if g.StatePath == "" {
		g.StatePath = ".syncerd-git-state.json"
	}
	if g.Schedule == "" {
		g.Schedule = "0 */6 * * *"
	}
	if g.Concurrency <= 0 {
		g.Concurrency = 4
	}
	for i := range g.Mirrors {
		m := &g.Mirrors[i]
		if m.Visibility == "" {
			m.Visibility = "private"
		}
		if m.PushMode == "" {
			m.PushMode = "mirror"
		}
		if m.NameTemplate == "" {
			m.NameTemplate = "{{ .Repo }}"
		}
	}
}

// ApplyEnvOverlay fills empty provider secrets from the environment, using
// SYNCERD_GIT_<NAME>_TOKEN and SYNCERD_GIT_<NAME>_GIT_PASSWORD. Provider
// names are upper cased with non alphanumeric characters mapped to
// underscore. Values set in the config file win, so a deployment can pin a
// secret explicitly.
//
// viper cannot bind environment variables to dynamic list indices, which is
// why this overlay exists rather than a set of BindEnv calls.
func (g *GitConfig) ApplyEnvOverlay() {
	if g == nil {
		return
	}
	for i := range g.Providers {
		p := &g.Providers[i]
		key := envKeyFragment(p.Name)
		if p.Token == "" {
			p.Token = os.Getenv("SYNCERD_GIT_" + key + "_TOKEN")
		}
		if p.GitPassword == "" {
			p.GitPassword = os.Getenv("SYNCERD_GIT_" + key + "_GIT_PASSWORD")
		}
		if p.GitUsername == "" {
			p.GitUsername = os.Getenv("SYNCERD_GIT_" + key + "_GIT_USERNAME")
		}
	}
}

// envKeyFragment converts a provider name into an environment variable
// fragment: "gh-mirrors" becomes "GH_MIRRORS".
func envKeyFragment(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

// ValidateGitSync checks the configuration required by the git-sync
// command. It is never called by the sync command.
func (c *Config) ValidateGitSync() error {
	g := c.Git
	if g == nil {
		return fmt.Errorf("git configuration is required for git-sync")
	}
	if len(g.Providers) == 0 {
		return fmt.Errorf("at least one git provider is required")
	}
	if len(g.Mirrors) == 0 {
		return fmt.Errorf("at least one mirror is required")
	}

	seenProviders := make(map[string]int, len(g.Providers))
	seenEnvKeys := make(map[string]string, len(g.Providers))
	for i, p := range g.Providers {
		if p.Name == "" {
			return fmt.Errorf("git.providers[%d].name is required", i)
		}
		if j, ok := seenProviders[p.Name]; ok {
			return fmt.Errorf("git.providers[%d].name %q duplicates git.providers[%d].name; names must be unique", i, p.Name, j)
		}
		seenProviders[p.Name] = i

		envKey := envKeyFragment(p.Name)
		if other, ok := seenEnvKeys[envKey]; ok {
			return fmt.Errorf("git.providers[%d].name %q and provider %q both map to the environment prefix SYNCERD_GIT_%s; provider names must remain distinct after non alphanumeric characters are replaced with underscores", i, p.Name, other, envKey)
		}
		seenEnvKeys[envKey] = p.Name

		if p.Type == "" {
			return fmt.Errorf("git.providers[%d].type is required", i)
		}
		if !supportedProviderTypes[p.Type] {
			return fmt.Errorf("git.providers[%d].type %q is an unsupported provider type", i, p.Type)
		}
		if err := validateProviderFields(i, p); err != nil {
			return err
		}
	}

	seenMirrors := make(map[string]int, len(g.Mirrors))
	for i, m := range g.Mirrors {
		if m.Name == "" {
			return fmt.Errorf("git.mirrors[%d].name is required", i)
		}
		if j, ok := seenMirrors[m.Name]; ok {
			return fmt.Errorf("git.mirrors[%d].name %q duplicates git.mirrors[%d].name; names must be unique (used as the state key)", i, m.Name, j)
		}
		seenMirrors[m.Name] = i

		if _, ok := g.Provider(m.Source); !ok {
			return fmt.Errorf("git.mirrors[%d].source %q refers to an unknown provider", i, m.Source)
		}
		dst, ok := g.Provider(m.Destination)
		if !ok {
			return fmt.Errorf("git.mirrors[%d].destination %q refers to an unknown provider", i, m.Destination)
		}
		if m.Source == m.Destination {
			return fmt.Errorf("git.mirrors[%d] has the same source and destination %q", i, m.Source)
		}

		if m.PushMode != "" && !validPushModes[m.PushMode] {
			return fmt.Errorf("git.mirrors[%d].push_mode %q is invalid: want mirror, additive, or fast-forward", i, m.PushMode)
		}

		if m.Visibility != "" && !validVisibilities[m.Visibility] {
			return fmt.Errorf("git.mirrors[%d].visibility %q is invalid: want private or public", i, m.Visibility)
		}

		if m.NameTemplate != "" {
			tpl, err := vcs.ParseNameTemplate(m.NameTemplate)
			if err != nil {
				return fmt.Errorf("git.mirrors[%d].name_template is invalid: %w", i, err)
			}
			if tpl.ProducesNestedName() && flatProviderTypes[dst.Type] {
				return fmt.Errorf("git.mirrors[%d].name_template renders a nested name but destination type %q does not support nested repository paths", i, dst.Type)
			}
		}

		// A disabled block is not validated. Its fields have no effect,
		// and rejecting a stale value in one would block a run that does
		// not read it.
		if m.PullRequests.MirrorObjects && !m.PullRequests.Enabled {
			return fmt.Errorf("git.mirrors[%d].pull_requests.mirror_objects is set but enabled is not; the destination pull request needs the head branch that enabled mirrors", i)
		}
		if m.PullRequests.Enabled {
			if err := vcs.ValidateBranchPrefix(m.PullRequests.BranchPrefixOrDefault()); err != nil {
				return fmt.Errorf("git.mirrors[%d].pull_requests: %w", i, err)
			}
			for _, s := range m.PullRequests.States {
				if s != "open" {
					return fmt.Errorf("git.mirrors[%d].pull_requests.states %q is not supported: only open is accepted, because mirroring a closed or merged pull request needs the destination pull request objects that arrive in a later release", i, s)
				}
			}
		}
	}

	return nil
}

// validateProviderFields enforces the per type required fields.
func validateProviderFields(i int, p GitProviderConfig) error {
	switch p.Type {
	case "github", "gitlab":
		if p.Owner == "" {
			return fmt.Errorf("git.providers[%d].owner is required for type %q", i, p.Type)
		}
		if p.Token == "" {
			return fmt.Errorf("git.providers[%d].token is required for type %q; set it in the config or as SYNCERD_GIT_%s_TOKEN", i, p.Type, envKeyFragment(p.Name))
		}
	case "bitbucket":
		if p.Owner == "" {
			return fmt.Errorf("git.providers[%d].owner is required for type %q", i, p.Type)
		}
		if p.Email == "" {
			return fmt.Errorf("git.providers[%d].email is required for bitbucket: app passwords were retired on 2026-07-28 and API tokens authenticate with the account email", i)
		}
		if p.Token == "" {
			return fmt.Errorf("git.providers[%d].token is required for bitbucket; set it in the config or as SYNCERD_GIT_%s_TOKEN", i, envKeyFragment(p.Name))
		}
	case "azuredevops":
		if p.Owner == "" {
			return fmt.Errorf("git.providers[%d].owner is required for type %q", i, p.Type)
		}
		if p.Project == "" {
			return fmt.Errorf("git.providers[%d].project is required for azuredevops: repositories live inside a project and SyncerD does not create projects", i)
		}
		if p.Auth != "" && p.Auth != "pat" && p.Auth != "entra" {
			return fmt.Errorf("git.providers[%d].auth %q is invalid for azuredevops: want pat or entra", i, p.Auth)
		}
		// A token is required in both modes. In pat mode it is an org
		// scoped personal access token. In entra mode it is a Microsoft
		// Entra ID access token that the operator obtains and supplies:
		// SyncerD does not acquire one itself.
		if p.Token == "" {
			if p.Auth == "entra" {
				return fmt.Errorf("git.providers[%d].token is required for azuredevops in entra mode: it is a Microsoft Entra ID access token the operator obtains and supplies, since SyncerD does not acquire one; set it in the config or as SYNCERD_GIT_%s_TOKEN", i, envKeyFragment(p.Name))
			}
			return fmt.Errorf("git.providers[%d].token is required for azuredevops in pat mode: it is an org scoped personal access token; set it in the config or as SYNCERD_GIT_%s_TOKEN, or set auth: entra", i, envKeyFragment(p.Name))
		}
	case "codecommit":
		if p.Region == "" {
			return fmt.Errorf("git.providers[%d].region is required for codecommit", i)
		}
		// The API half can authenticate through the IAM role, but the git
		// transport cannot: SyncerD does not derive SigV4 git credentials,
		// so a static IAM HTTPS Git credential pair is required.
		if p.GitUsername == "" || p.GitPassword == "" {
			return fmt.Errorf("git.providers[%d] requires git_username and git_password for codecommit: SyncerD does not derive SigV4 git credentials, so generate IAM HTTPS Git credentials in the IAM console and set both fields", i)
		}
	}
	return nil
}
