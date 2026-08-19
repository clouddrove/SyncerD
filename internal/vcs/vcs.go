// Package vcs abstracts the git hosting providers SyncerD can mirror
// between. Roles are separate interfaces so configuration can be proven
// runnable before any network call is made.
package vcs

import (
	"context"
	"net/url"
)

// Repo is a repository as reported by a source provider.
type Repo struct {
	Owner         string
	Name          string
	Path          string // namespace path, may contain slashes on GitLab
	CloneURL      string // HTTPS git URL as reported by the provider
	DefaultBranch string
	Archived      bool
	Fork          bool
	Empty         bool
}

// RepoSpec describes a repository to create at a destination.
type RepoSpec struct {
	Path          string
	DefaultBranch string
	Visibility    string
}

// CredKind selects how a credential is presented to git.
type CredKind int

const (
	CredBasic CredKind = iota
	CredBearer
)

// GitCredential authenticates the git transport.
type GitCredential struct {
	Kind   CredKind
	User   string // basic only
	Secret string
}

// Lister discovers repositories. Implemented by source providers.
type Lister interface {
	ListRepos(ctx context.Context) ([]Repo, error)
}

// Ensurer creates a repository if it does not exist. Implemented by
// destination providers that support create_missing.
type Ensurer interface {
	EnsureRepo(ctx context.Context, spec RepoSpec) (Repo, error)
}

// Remote supplies everything needed to talk git to a provider.
type Remote interface {
	// CloneURL builds the HTTPS git URL for a destination repository name
	// that is relative to the provider's configured owner. It is not used
	// for source repositories, which carry their own CloneURL from
	// ListRepos.
	CloneURL(name string) string
	// QualifiedPath turns an owner relative destination name into the same
	// fully qualified form ListRepos reports in Repo.Path.
	//
	// Two conventions meet here and used to collide silently. A rendered
	// destination name is owner relative, because CloneURL and EnsureRepo
	// prepend the owner themselves. Every API that addresses a repository
	// wants the qualified path. Without this, a destination pull request
	// call built "/repos/widget/pulls" instead of "/repos/acme/widget/pulls"
	// and answered 404 for every repository.
	QualifiedPath(name string) string
	// GitCredential is called per run because Entra tokens and AWS
	// credentials are short lived.
	GitCredential(ctx context.Context) (GitCredential, error)
	SupportsNesting() bool
}

// DefaultBranchSetter aligns a freshly created destination repository with
// the source default branch. Optional.
type DefaultBranchSetter interface {
	SetDefaultBranch(ctx context.Context, path, branch string) error
}

// Provider is the minimum every provider implements.
type Provider interface {
	Name() string
	Type() string
	Remote
}

// SanitizeCloneURL removes any userinfo a provider API embedded in a clone
// URL. Azure DevOps returns https://{org}@dev.azure.com/... and Bitbucket
// returns https://{account}@bitbucket.org/..., and the git runner refuses a
// URL carrying userinfo, because for an operator supplied URL that would
// mean a credential in config. Credentials reach git through
// GitCredential, never through the URL.
func SanitizeCloneURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
