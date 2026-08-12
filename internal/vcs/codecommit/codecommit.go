// Package codecommit implements the AWS CodeCommit provider for git
// mirroring.
//
// CodeCommit has two halves that authenticate differently, and this
// provider deliberately covers only one of them fully.
//
// The API half (listing and creating repositories) goes through the AWS
// SDK, which signs every request with SigV4 itself. Credentials resolve
// through the normal AWS chain: environment variables, shared config,
// IRSA, or an instance role. No signing is implemented here.
//
// The git transport half uses static IAM HTTPS Git credentials only: the
// username and password pair generated in the IAM console for a user, sent
// as plain HTTP basic auth. AWS also supports a credential helper that
// derives a per request SigV4 password so IRSA and instance roles work for
// git too, without a static secret. SyncerD does not implement that
// derivation. It would be a real improvement, but its exact wire format
// cannot be verified without a live CodeCommit endpoint, and shipping
// unverifiable credential derivation is worse than shipping a documented
// limitation. If SyncerD's operator credentials are not IAM HTTPS Git
// credentials, GitCredential returns an actionable error rather than
// guessing.
package codecommit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/codecommit"
	"github.com/aws/aws-sdk-go-v2/service/codecommit/types"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// api is the subset of the CodeCommit API this provider uses. Keeping it
// narrow lets tests supply a fake without reaching AWS.
type api interface {
	ListRepositories(ctx context.Context, in *codecommit.ListRepositoriesInput, optFns ...func(*codecommit.Options)) (*codecommit.ListRepositoriesOutput, error)
	GetRepository(ctx context.Context, in *codecommit.GetRepositoryInput, optFns ...func(*codecommit.Options)) (*codecommit.GetRepositoryOutput, error)
	CreateRepository(ctx context.Context, in *codecommit.CreateRepositoryInput, optFns ...func(*codecommit.Options)) (*codecommit.CreateRepositoryOutput, error)
	UpdateDefaultBranch(ctx context.Context, in *codecommit.UpdateDefaultBranchInput, optFns ...func(*codecommit.Options)) (*codecommit.UpdateDefaultBranchOutput, error)
}

// clientFactory builds the api client. It exists so tests can substitute a
// fake without any network or credential access; production code always
// uses defaultClientFactory.
type clientFactory func(ctx context.Context, region string) (api, error)

// defaultClientFactory resolves AWS credentials through the standard chain
// (environment, shared config, IRSA, instance role) and returns a real
// CodeCommit client. This can hit the network, which is why construction is
// deferred to first use rather than done in New.
func defaultClientFactory(ctx context.Context, region string) (api, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("codecommit: load AWS configuration: %w", err)
	}
	return codecommit.NewFromConfig(cfg), nil
}

// Config constructs a Provider.
type Config struct {
	Name string
	// Region is the AWS region CodeCommit repositories live in. There is no
	// owner: CodeCommit repository names are flat within an account and
	// region.
	Region string
	// GitUsername and GitPassword are IAM HTTPS Git credentials, generated
	// in the IAM console. Both are required together for GitCredential to
	// succeed; config validation enforces they are set together or not at
	// all.
	GitUsername string
	GitPassword string
}

// Provider talks to the AWS CodeCommit API and to CodeCommit over git.
//
// A Provider must not be copied after first use.
type Provider struct {
	name        string
	region      string
	gitUsername string
	gitPassword string
	factory     clientFactory

	clientMu sync.Mutex
	client   api
}

// New validates cfg and returns a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("codecommit provider %q: region is required", cfg.Name)
	}
	return &Provider{
		name:        cfg.Name,
		region:      cfg.Region,
		gitUsername: cfg.GitUsername,
		gitPassword: cfg.GitPassword,
		factory:     defaultClientFactory,
	}, nil
}

// Name returns the configured provider name.
func (p *Provider) Name() string { return p.name }

// Type returns the provider type string.
func (p *Provider) Type() string { return "codecommit" }

// SupportsNesting reports false. CodeCommit repository names are flat
// within an account and region; there is no namespace to nest under.
func (p *Provider) SupportsNesting() bool { return false }

// CloneURL returns the HTTPS git URL for a repository name in the
// configured region. The name is relative to the provider, matching what
// EnsureRepo creates.
//
// This is only used for a destination name; it assumes a bare name with no
// slash. A nested name is rejected upstream by SupportsNesting before the
// engine ever calls this method, so no guard is repeated here.
func (p *Provider) CloneURL(name string) string {
	return fmt.Sprintf("https://git-codecommit.%s.amazonaws.com/v1/repos/%s", p.region, name)
}

// GitCredential returns the basic auth pair for IAM HTTPS Git credentials.
// If none are configured, this returns an actionable error naming the
// limitation described in the package doc comment: SyncerD does not derive
// SigV4 git credentials, so a static IAM HTTPS Git credential pair is
// required for the git transport even when the API half authenticates via
// IRSA or an instance role.
func (p *Provider) GitCredential(context.Context) (vcs.GitCredential, error) {
	if p.gitUsername == "" || p.gitPassword == "" {
		return vcs.GitCredential{}, fmt.Errorf("codecommit provider %q has no git credentials; generate IAM HTTPS Git credentials and set git_username and git_password, since SyncerD does not derive SigV4 git credentials", p.name)
	}
	return vcs.GitCredential{Kind: vcs.CredBasic, User: p.gitUsername, Secret: p.gitPassword}, nil
}

// clientCached returns the cached api client, constructing it on first use.
// The lock is held across the whole check and fill so concurrent callers
// resolve it once rather than racing on the cache; the engine calls
// EnsureRepo and ListRepos from several worker goroutines at once.
// Construction can hit the network for credential resolution, so a
// transient failure is not cached and a later call can retry.
func (p *Provider) clientCached(ctx context.Context) (api, error) {
	p.clientMu.Lock()
	defer p.clientMu.Unlock()

	if p.client != nil {
		return p.client, nil
	}

	c, err := p.factory(ctx, p.region)
	if err != nil {
		return nil, err
	}
	p.client = c
	return p.client, nil
}

// toRepo converts RepositoryMetadata to vcs.Repo.
func toRepo(m *types.RepositoryMetadata) vcs.Repo {
	if m == nil {
		return vcs.Repo{}
	}

	name := ""
	if m.RepositoryName != nil {
		name = *m.RepositoryName
	}
	var branch string
	if m.DefaultBranch != nil {
		branch = *m.DefaultBranch
	}
	var cloneURL string
	if m.CloneUrlHttp != nil {
		cloneURL = *m.CloneUrlHttp
	}

	return vcs.Repo{
		// There is no owner concept in CodeCommit, so Owner is left empty.
		Name: name,
		// Name and Path are both the flat repository name: there is no
		// namespace.
		Path:          name,
		CloneURL:      cloneURL,
		DefaultBranch: branch,
		// CodeCommit has no archived concept for repositories, so this is
		// always false. skip_archived therefore has no effect on a
		// CodeCommit source, which is correct behaviour, not an oversight.
		Archived: false,
		// CodeCommit has no fork concept for repositories, so this is
		// always false. skip_forks therefore has no effect on a CodeCommit
		// source, which is correct behaviour, not an oversight.
		Fork: false,
		// A nil DefaultBranch is the only available signal that a
		// repository has no commits yet, so it is used as the Empty
		// signal.
		Empty: m.DefaultBranch == nil,
	}
}

// maxPages bounds a paginated listing, stopping a server that returns a
// cyclic next token. This allows far more pages than any real account
// would ever produce.
const maxPages = 1000

// listWorkers bounds how many GetRepository calls run concurrently while
// enriching a listing.
const listWorkers = 6

// ListRepos returns every repository in the configured region.
//
// ListRepositories returns only a name and id per repository: it does not
// carry the default branch, the clone URL, or whether a repository is
// empty. Filling vcs.Repo requires a GetRepository call per repository,
// which is an N+1 pattern this provider accepts as unavoidable given the
// API shape. The enrichment calls run through a small bounded worker pool
// rather than serially or all at once, and results are written into an
// indexed slice so ordering matches the listing regardless of which
// worker finishes first.
func (p *Provider) ListRepos(ctx context.Context) ([]vcs.Repo, error) {
	c, err := p.clientCached(ctx)
	if err != nil {
		return nil, fmt.Errorf("codecommit: construct client: %w", err)
	}

	var summaries []types.RepositoryNameIdPair
	var token *string
	seen := make(map[string]bool)

	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf("codecommit: pagination exceeded %d pages, refusing to continue", maxPages)
		}

		out, err := c.ListRepositories(ctx, &codecommit.ListRepositoriesInput{NextToken: token})
		if err != nil {
			return nil, fmt.Errorf("codecommit: list repositories: %w", err)
		}
		summaries = append(summaries, out.Repositories...)

		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		if seen[*out.NextToken] {
			return nil, fmt.Errorf("codecommit: pagination revisited a next token, refusing to loop")
		}
		seen[*out.NextToken] = true
		token = out.NextToken
	}

	repos := make([]vcs.Repo, len(summaries))
	errs := make([]error, len(summaries))

	sem := make(chan struct{}, listWorkers)
	var wg sync.WaitGroup
	for i, s := range summaries {
		name := ""
		if s.RepositoryName != nil {
			name = *s.RepositoryName
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(i int, name string) {
			defer wg.Done()
			defer func() { <-sem }()

			out, err := c.GetRepository(ctx, &codecommit.GetRepositoryInput{RepositoryName: &name})
			if err != nil {
				errs[i] = fmt.Errorf("codecommit: get repository %q: %w", name, err)
				return
			}
			repos[i] = toRepo(out.RepositoryMetadata)
		}(i, name)
	}
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return repos, nil
}

// EnsureRepo creates the repository if it does not already exist.
//
// spec.Visibility is ignored: CodeCommit repositories have no visibility
// setting, so there is nothing to apply it to.
func (p *Provider) EnsureRepo(ctx context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	name := spec.Path
	if strings.Contains(name, "/") {
		return vcs.Repo{}, fmt.Errorf("codecommit: repository name %q contains a slash, which CodeCommit does not support: repository names are flat within an account and region", name)
	}

	c, err := p.clientCached(ctx)
	if err != nil {
		return vcs.Repo{}, fmt.Errorf("codecommit: construct client: %w", err)
	}

	out, err := c.GetRepository(ctx, &codecommit.GetRepositoryInput{RepositoryName: &name})
	if err == nil {
		return toRepo(out.RepositoryMetadata), nil
	}

	var notFound *types.RepositoryDoesNotExistException
	if !errors.As(err, &notFound) {
		return vcs.Repo{}, fmt.Errorf("codecommit: get repository %q: %w", name, err)
	}

	created, err := c.CreateRepository(ctx, &codecommit.CreateRepositoryInput{RepositoryName: &name})
	if err != nil {
		return vcs.Repo{}, fmt.Errorf("codecommit: create repository %q: %w", name, err)
	}
	return toRepo(created.RepositoryMetadata), nil
}

// SetDefaultBranch aligns the destination default branch with the source.
func (p *Provider) SetDefaultBranch(ctx context.Context, path, branch string) error {
	name := path
	if strings.Contains(name, "/") {
		return fmt.Errorf("codecommit: repository name %q contains a slash, which CodeCommit does not support: repository names are flat within an account and region", name)
	}

	c, err := p.clientCached(ctx)
	if err != nil {
		return fmt.Errorf("codecommit: construct client: %w", err)
	}

	_, err = c.UpdateDefaultBranch(ctx, &codecommit.UpdateDefaultBranchInput{
		RepositoryName:    &name,
		DefaultBranchName: &branch,
	})
	if err != nil {
		return fmt.Errorf("codecommit: set default branch for %q: %w", name, err)
	}
	return nil
}

var (
	_ vcs.Lister              = (*Provider)(nil)
	_ vcs.Ensurer             = (*Provider)(nil)
	_ vcs.Remote              = (*Provider)(nil)
	_ vcs.DefaultBranchSetter = (*Provider)(nil)
)
