// Package azuredevops implements the Azure DevOps Repos provider for git
// mirroring.
//
// Authentication has two operator supplied modes, selected by the
// provider's auth field ("pat", the default, or "entra").
//
//   - pat mode presents the configured token as HTTP basic auth with an
//     empty username, which is what Azure DevOps expects of a personal
//     access token.
//   - entra mode presents the configured token as a bearer token. That
//     token is a Microsoft Entra ID access token the OPERATOR supplies
//     through the same SYNCERD_GIT_<NAME>_TOKEN mechanism as a PAT.
//
// SyncerD never acquires an Entra token itself; it only presents the one it
// was given. This is the obvious thing for a future maintainer to get
// wrong, so it bears repeating: there is no Azure AD client here, no
// refresh logic, and no token minting. Operators obtain the token via the
// az CLI, a workload identity sidecar, or a service principal, and refresh
// it themselves on whatever cadence their token's lifetime demands.
package azuredevops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

const defaultAPIURL = "https://dev.azure.com"

// apiVersion is sent on every request. Azure DevOps requires an explicit
// api-version and this is the version this provider was built against.
const apiVersion = "7.1"

// Authentication modes accepted by the auth config field.
const (
	authPAT   = "pat"
	authEntra = "entra"
)

// Config constructs a Provider.
type Config struct {
	Name       string
	Owner      string // Azure DevOps organization
	Project    string // repositories live inside a project
	APIURL     string
	Auth       string // "pat" (default) or "entra"
	Token      string
	HTTPClient *http.Client
}

// Provider talks to the Azure DevOps Repos REST API and to dev.azure.com
// over git.
//
// A Provider must not be copied after first use.
type Provider struct {
	name    string
	org     string
	project string
	apiURL  string
	auth    string
	token   string
	hc      *http.Client

	projectMu sync.Mutex
	projectID *string // GUID, resolved once and cached; nil until resolved
}

// New validates cfg and returns a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Owner == "" {
		return nil, fmt.Errorf("azuredevops provider %q: owner (organization) is required", cfg.Name)
	}
	if cfg.Project == "" {
		return nil, fmt.Errorf("azuredevops provider %q: project is required: repositories live inside a project and SyncerD does not create projects", cfg.Name)
	}

	auth := cfg.Auth
	if auth == "" {
		auth = authPAT
	}
	if auth != authPAT && auth != authEntra {
		return nil, fmt.Errorf("azuredevops provider %q: auth %q is invalid, want %q or %q", cfg.Name, cfg.Auth, authPAT, authEntra)
	}
	if auth == authPAT && cfg.Token == "" {
		return nil, fmt.Errorf("azuredevops provider %q: token is required in pat mode", cfg.Name)
	}

	api := strings.TrimRight(cfg.APIURL, "/")
	if api == "" {
		api = defaultAPIURL
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}

	return &Provider{
		name:    cfg.Name,
		org:     cfg.Owner,
		project: cfg.Project,
		apiURL:  api,
		auth:    auth,
		token:   cfg.Token,
		hc:      hc,
	}, nil
}

// Name returns the configured provider name.
func (p *Provider) Name() string { return p.name }

// Type returns the provider type string.
func (p *Provider) Type() string { return "azuredevops" }

// SupportsNesting reports false. An Azure DevOps repository name cannot
// contain a slash: the only namespaces are the organization and the
// project, both configured separately.
func (p *Provider) SupportsNesting() bool { return false }

// CloneURL returns the HTTPS git URL for a repository name under the
// configured organization and project. The name is relative to the
// project, matching what EnsureRepo creates.
//
// This is only used for a destination name; it assumes a bare name with no
// slash. A nested name is rejected upstream by SupportsNesting before the
// engine ever calls this method, so no guard is repeated here.
// QualifiedPath prepends the configured project, matching the project/name
// form ListRepos reports. The organisation is not part of it: it is a
// separate segment of every URL rather than part of the repository path.
func (p *Provider) QualifiedPath(name string) string {
	return p.project + "/" + strings.Trim(name, "/")
}

func (p *Provider) CloneURL(name string) string {
	return fmt.Sprintf("%s/%s/%s/_git/%s", p.apiURL, p.org, p.project, name)
}

// GitCredential returns the credential git should present, matching the
// configured auth mode.
//
// pat mode returns HTTP basic auth with the PAT as the password. Azure
// DevOps ignores the username on this path and authenticates on the token
// alone, so the value is free; the organisation name is used because it
// makes the credential legible without being a secret.
//
// It must not be empty, which is what it used to be. SyncerD hands git its
// credential through a helper that prints "username=<value>", and git reads
// an empty value as no username at all: it then tries to ask, terminal
// prompts are disabled, and the clone fails with "could not read Username"
// rather than anything pointing at the configuration.
//
// entra mode returns a bearer credential carrying the operator supplied
// Entra access token. If entra mode is configured but no token was ever
// supplied, this returns an error rather than presenting an empty bearer
// token to git.
func (p *Provider) GitCredential(context.Context) (vcs.GitCredential, error) {
	if p.auth == authEntra {
		if p.token == "" {
			return vcs.GitCredential{}, fmt.Errorf("azuredevops: entra mode requires a token supplied by the operator; none was configured")
		}
		return vcs.GitCredential{Kind: vcs.CredBearer, Secret: p.token}, nil
	}
	return vcs.GitCredential{Kind: vcs.CredBasic, User: p.org, Secret: p.token}, nil
}

// apiRepo is the subset of the Azure DevOps repository object SyncerD
// reads.
type apiRepo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"` // full ref, e.g. refs/heads/main; absent on an empty repository
	RemoteURL     string `json:"remoteUrl"`
	IsDisabled    bool   `json:"isDisabled"`
	IsFork        bool   `json:"isFork"`
	Size          int64  `json:"size"`
}

// toRepo converts the API representation to vcs.Repo. org and project are
// passed in rather than read off the Provider, since apiRepo has no access
// to it.
func (a apiRepo) toRepo(org, project, apiURL string) vcs.Repo {
	branch := strings.TrimPrefix(a.DefaultBranch, "refs/heads/")

	cloneURL := vcs.SanitizeCloneURL(a.RemoteURL)
	if cloneURL == "" {
		cloneURL = fmt.Sprintf("%s/%s/%s/_git/%s", apiURL, org, project, a.Name)
	}

	return vcs.Repo{
		Owner:         org,
		Name:          a.Name,
		Path:          project + "/" + a.Name,
		CloneURL:      cloneURL,
		DefaultBranch: branch,
		// isDisabled is the closest Azure DevOps equivalent to archived: a
		// disabled repository should be skippable by skip_archived.
		Archived: a.IsDisabled,
		Fork:     a.IsFork,
		Empty:    a.Size == 0,
	}
}

// repoListResponse is the envelope Azure DevOps wraps a repository listing
// in. It is an object carrying a count and a value array, not a bare array.
type repoListResponse struct {
	Count int       `json:"count"`
	Value []apiRepo `json:"value"`
}

// maxPages bounds a paginated listing, stopping a server that returns a
// cyclic continuation token. This allows far more pages than any real
// project would ever produce.
const maxPages = 1000

// ListRepos returns every repository in the configured project. Azure
// DevOps normally returns the full list in one response for this endpoint;
// if a response carries an x-ms-continuationtoken header, the next page is
// fetched by adding a continuationToken query parameter, bounded by
// maxPages and guarded against a token that repeats.
func (p *Provider) ListRepos(ctx context.Context) ([]vcs.Repo, error) {
	base := fmt.Sprintf("%s/%s/%s/_apis/git/repositories", p.apiURL, p.org, p.project)

	var out []vcs.Repo
	seen := make(map[string]bool)
	token := ""

	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("azuredevops: pagination exceeded %d pages, refusing to continue", maxPages)
		}

		q := url.Values{"api-version": {apiVersion}}
		if token != "" {
			q.Set("continuationToken", token)
		}
		reqURL := base + "?" + q.Encode()

		body, header, err := p.do(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return out, err
		}

		var page repoListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("azuredevops: decode repository list: %w", err)
		}
		for _, r := range page.Value {
			out = append(out, r.toRepo(p.org, p.project, p.apiURL))
		}

		next := strings.TrimSpace(header.Get("x-ms-continuationtoken"))
		if next == "" {
			return out, nil
		}
		if seen[next] {
			return out, fmt.Errorf("azuredevops: pagination revisited a continuation token, refusing to loop")
		}
		seen[next] = true
		token = next
	}
}

// getRepo fetches one repository by name. A 404 surfaces as isNotFound.
func (p *Provider) getRepo(ctx context.Context, name string) (apiRepo, error) {
	repoURL := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s?api-version=%s", p.apiURL, p.org, p.project, name, apiVersion)
	body, _, err := p.do(ctx, http.MethodGet, repoURL, nil)
	if err != nil {
		return apiRepo{}, err
	}
	var out apiRepo
	if err := json.Unmarshal(body, &out); err != nil {
		return apiRepo{}, fmt.Errorf("azuredevops: decode repository: %w", err)
	}
	return out, nil
}

// projectResponse is the subset of the Azure DevOps project object SyncerD
// reads.
type projectResponse struct {
	ID string `json:"id"`
}

// projectIDCached resolves the configured project's GUID, needed to create
// a repository. The result is cached because it cannot change during a run
// and creation may be called many times across the worker pool.
//
// The lock is held across the lookup so concurrent callers resolve it once
// rather than racing on the cache; the engine calls EnsureRepo from several
// worker goroutines at once. A transient failure is not cached, so a later
// call can retry.
func (p *Provider) projectIDCached(ctx context.Context) (string, error) {
	p.projectMu.Lock()
	defer p.projectMu.Unlock()

	if p.projectID != nil {
		return *p.projectID, nil
	}

	projURL := fmt.Sprintf("%s/%s/_apis/projects/%s?api-version=%s", p.apiURL, p.org, p.project, apiVersion)
	body, _, err := p.do(ctx, http.MethodGet, projURL, nil)
	if err != nil {
		return "", err
	}

	var pr projectResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return "", fmt.Errorf("azuredevops: decode project: %w", err)
	}
	if pr.ID == "" {
		return "", fmt.Errorf("azuredevops: project %q lookup returned no id", p.project)
	}

	id := pr.ID
	p.projectID = &id
	return id, nil
}

// EnsureRepo creates the repository if it does not already exist.
func (p *Provider) EnsureRepo(ctx context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	name := spec.Path
	if strings.Contains(name, "/") {
		return vcs.Repo{}, fmt.Errorf("azuredevops: repository name %q contains a slash, which Azure DevOps does not support; the organization and project are configured separately", name)
	}

	existing, err := p.getRepo(ctx, name)
	if err == nil {
		return existing.toRepo(p.org, p.project, p.apiURL), nil
	}
	if !isNotFound(err) {
		return vcs.Repo{}, err
	}

	projectID, err := p.projectIDCached(ctx)
	if err != nil {
		return vcs.Repo{}, fmt.Errorf("azuredevops: look up project %q: %w", p.project, err)
	}

	createURL := fmt.Sprintf("%s/%s/%s/_apis/git/repositories?api-version=%s", p.apiURL, p.org, p.project, apiVersion)
	payload := map[string]any{
		"name": name,
		"project": map[string]any{
			"id": projectID,
		},
	}

	created, _, err := p.do(ctx, http.MethodPost, createURL, payload)
	if err != nil {
		return vcs.Repo{}, err
	}

	var out apiRepo
	if err := json.Unmarshal(created, &out); err != nil {
		return vcs.Repo{}, fmt.Errorf("azuredevops: decode created repository: %w", err)
	}
	return out.toRepo(p.org, p.project, p.apiURL), nil
}

// SetDefaultBranch aligns the destination default branch with the source.
// The update endpoint needs the repository GUID, not its name, so the
// repository is looked up first; the value sent must be the full ref, so
// the refs/heads/ prefix stripped when listing is re-added here.
func (p *Provider) SetDefaultBranch(ctx context.Context, path, branch string) error {
	name := path
	if strings.Contains(name, "/") {
		return fmt.Errorf("azuredevops: repository name %q contains a slash, which Azure DevOps does not support; the organization and project are configured separately", name)
	}

	repo, err := p.getRepo(ctx, name)
	if err != nil {
		return fmt.Errorf("azuredevops: look up repository %q to set default branch: %w", name, err)
	}

	patchURL := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s?api-version=%s", p.apiURL, p.org, p.project, repo.ID, apiVersion)
	payload := map[string]any{"defaultBranch": "refs/heads/" + branch}
	_, _, err = p.do(ctx, http.MethodPatch, patchURL, payload)
	return err
}

// httpError carries a non 2xx response.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("azuredevops: HTTP %d: %s", e.status, e.body)
}

// isNotFound reports whether err is an Azure DevOps 404, unwrapping any
// context added along the way.
func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.status == http.StatusNotFound
}

// do performs a request and returns the body. The token never appears in
// the returned error, including the base64 encoded basic auth header: a
// failed request's error carries only the status and response body, never
// the request it sent.
func (p *Provider) do(ctx context.Context, method, reqURL string, payload any) ([]byte, http.Header, error) {
	if p.auth == authEntra && p.token == "" {
		return nil, nil, fmt.Errorf("azuredevops: entra mode requires a token supplied by the operator; none was configured")
	}

	var reader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("azuredevops: encode request: %w", err)
		}
		reader = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("azuredevops: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	switch p.auth {
	case authEntra:
		req.Header.Set("Authorization", "Bearer "+p.token)
	default:
		// pat mode: HTTP basic auth with an empty username and the PAT as
		// the password, built explicitly per the Azure DevOps convention.
		encoded := base64.StdEncoding.EncodeToString([]byte(":" + p.token))
		req.Header.Set("Authorization", "Basic "+encoded)
	}

	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("azuredevops: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("azuredevops: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.Header, &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	return body, resp.Header, nil
}

var (
	_ vcs.Lister              = (*Provider)(nil)
	_ vcs.Ensurer             = (*Provider)(nil)
	_ vcs.Remote              = (*Provider)(nil)
	_ vcs.DefaultBranchSetter = (*Provider)(nil)
)
