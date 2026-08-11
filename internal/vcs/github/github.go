// Package github implements the GitHub provider for git mirroring.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

const defaultAPIURL = "https://api.github.com"

// Config constructs a Provider.
type Config struct {
	Name       string
	Owner      string
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

// Provider talks to the GitHub REST API and to github.com over git.
//
// A Provider must not be copied after first use.
type Provider struct {
	name      string
	owner     string
	apiURL    string
	cloneBase string
	token     string
	hc        *http.Client

	ownerMu    sync.Mutex
	ownerIsOrg *bool
}

// New validates cfg and returns a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Owner == "" {
		return nil, fmt.Errorf("github provider %q: owner is required", cfg.Name)
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
		name:      cfg.Name,
		owner:     cfg.Owner,
		apiURL:    api,
		cloneBase: cloneBaseFor(api),
		token:     cfg.Token,
		hc:        hc,
	}, nil
}

// cloneBaseFor derives the web host used for git URLs from the API URL.
// GitHub Enterprise Server exposes its API under /api/v3 on the same host.
func cloneBaseFor(api string) string {
	if api == defaultAPIURL {
		return "https://github.com"
	}
	return strings.TrimSuffix(api, "/api/v3")
}

// Name returns the configured provider name.
func (p *Provider) Name() string { return p.name }

// Type returns the provider type string.
func (p *Provider) Type() string { return "github" }

// SupportsNesting reports false. A GitHub repository name cannot contain a
// slash: the only namespace is the owner, which is configured separately.
// A name template that renders a nested name is rejected before any call
// reaches this provider.
func (p *Provider) SupportsNesting() bool { return false }

// CloneURL returns the HTTPS git URL for a repository name under the
// configured owner. The name is relative to the owner, matching what
// EnsureRepo creates.
func (p *Provider) CloneURL(name string) string {
	return fmt.Sprintf("%s/%s/%s.git", p.cloneBase, p.owner, name)
}

// GitCredential returns the basic auth pair GitHub expects for HTTPS git.
func (p *Provider) GitCredential(context.Context) (vcs.GitCredential, error) {
	return vcs.GitCredential{Kind: vcs.CredBasic, User: "x-access-token", Secret: p.token}, nil
}

// apiRepo is the subset of the GitHub repository object SyncerD reads.
type apiRepo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Size          int    `json:"size"`
}

func (a apiRepo) toRepo(owner string) vcs.Repo {
	path := a.FullName
	if path == "" {
		path = owner + "/" + a.Name
	}
	return vcs.Repo{
		Owner:         owner,
		Name:          a.Name,
		Path:          path,
		DefaultBranch: a.DefaultBranch,
		Archived:      a.Archived,
		Fork:          a.Fork,
		Empty:         a.Size == 0,
	}
}

// ListRepos returns every repository under the configured owner, following
// pagination. If the owner is not an organisation, the user endpoint is
// used instead.
func (p *Provider) ListRepos(ctx context.Context) ([]vcs.Repo, error) {
	orgURL := fmt.Sprintf("%s/orgs/%s/repos?per_page=100&type=all", p.apiURL, p.owner)

	repos, err := p.listFrom(ctx, orgURL)
	if err == nil {
		return repos, nil
	}

	// A 404 on the very first page means the owner is not an organisation.
	// A 404 partway through is a real failure, not a reason to restart
	// against a different endpoint and lose what we already collected.
	if isNotFound(err) && len(repos) == 0 {
		userURL := fmt.Sprintf("%s/users/%s/repos?per_page=100&type=all", p.apiURL, p.owner)
		return p.listFrom(ctx, userURL)
	}

	return nil, err
}

// maxPages bounds a paginated listing. GitHub caps at 100 per page, so this
// allows 100000 repositories, far beyond any real org, while stopping a
// server that returns a cyclic next link.
const maxPages = 1000

// listFrom walks the Link header chain starting at pageURL. It returns
// whatever it collected alongside any error, so the caller can tell a
// first page failure from a failure partway through.
func (p *Provider) listFrom(ctx context.Context, pageURL string) ([]vcs.Repo, error) {
	var out []vcs.Repo
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("github: pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("github: pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, header, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}

		var page []apiRepo
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("github: decode repository list: %w", err)
		}
		for _, r := range page {
			out = append(out, r.toRepo(p.owner))
		}

		next, nerr := resolveNext(pageURL, header.Get("Link"))
		if nerr != nil {
			return out, nerr
		}
		pageURL = next
	}

	return out, nil
}

var nextLinkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextLink extracts the rel="next" URL from a Link header.
func nextLink(link string) string {
	m := nextLinkRe.FindStringSubmatch(link)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// resolveNext extracts the rel="next" URL and resolves it against the
// current page URL, so a relative link is handled the same as an absolute
// one.
func resolveNext(current, link string) (string, error) {
	next := nextLink(link)
	if next == "" {
		return "", nil
	}
	base, err := url.Parse(current)
	if err != nil {
		return "", fmt.Errorf("github: parse current page URL: %w", err)
	}
	ref, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("github: parse next page link: %w", err)
	}
	return base.ResolveReference(ref).String(), nil
}

// ownerKind reports whether the configured owner is an organisation. The
// answer is cached because it cannot change during a run, and both listing
// and creation need it. A 404 means the owner is not an org, which is the
// normal personal account case rather than an error.
//
// The lock is held across the lookup so that concurrent callers resolve it
// once rather than racing on the cache. The engine's worker pool shares one
// Provider across goroutines.
func (p *Provider) ownerKind(ctx context.Context) (bool, error) {
	p.ownerMu.Lock()
	defer p.ownerMu.Unlock()

	if p.ownerIsOrg != nil {
		return *p.ownerIsOrg, nil
	}

	_, _, err := p.do(ctx, http.MethodGet, fmt.Sprintf("%s/orgs/%s", p.apiURL, p.owner), nil)
	switch {
	case err == nil:
		isOrg := true
		p.ownerIsOrg = &isOrg
		return true, nil
	case isNotFound(err):
		isOrg := false
		p.ownerIsOrg = &isOrg
		return false, nil
	default:
		// A transient failure is not cached, so a later call can retry.
		return false, err
	}
}

// EnsureRepo creates the repository if it does not already exist.
func (p *Provider) EnsureRepo(ctx context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	name := spec.Path
	if strings.Contains(name, "/") {
		return vcs.Repo{}, fmt.Errorf("github: repository name %q contains a slash, which GitHub does not support; the owner is configured separately", name)
	}

	getURL := fmt.Sprintf("%s/repos/%s/%s", p.apiURL, p.owner, name)
	body, _, err := p.do(ctx, http.MethodGet, getURL, nil)
	if err == nil {
		var existing apiRepo
		if err := json.Unmarshal(body, &existing); err != nil {
			return vcs.Repo{}, fmt.Errorf("github: decode repository: %w", err)
		}
		return existing.toRepo(p.owner), nil
	}

	if !isNotFound(err) {
		return vcs.Repo{}, err
	}

	isOrg, err := p.ownerKind(ctx)
	if err != nil {
		return vcs.Repo{}, fmt.Errorf("github: determine whether %q is an organisation: %w", p.owner, err)
	}

	payload := map[string]any{
		"name":    name,
		"private": spec.Visibility != "public",
	}

	// GitHub has no endpoint that creates a repository under another
	// user's personal account. Organisations use POST /orgs/{org}/repos;
	// a personal account destination must use POST /user/repos, which
	// creates under the authenticated token's own account. If the
	// configured owner is a personal account that is not the token's
	// account, creation is impossible on GitHub by any route, and the
	// API error surfaces that per repository.
	createURL := p.apiURL + "/user/repos"
	if isOrg {
		createURL = fmt.Sprintf("%s/orgs/%s/repos", p.apiURL, p.owner)
	}

	created, _, err := p.do(ctx, http.MethodPost, createURL, payload)
	if err != nil {
		return vcs.Repo{}, err
	}

	var out apiRepo
	if err := json.Unmarshal(created, &out); err != nil {
		return vcs.Repo{}, fmt.Errorf("github: decode created repository: %w", err)
	}
	return out.toRepo(p.owner), nil
}

// SetDefaultBranch aligns the destination default branch with the source.
func (p *Provider) SetDefaultBranch(ctx context.Context, path, branch string) error {
	name := path
	if strings.Contains(name, "/") {
		return fmt.Errorf("github: repository name %q contains a slash, which GitHub does not support; the owner is configured separately", name)
	}
	url := fmt.Sprintf("%s/repos/%s/%s", p.apiURL, p.owner, name)
	_, _, err := p.do(ctx, http.MethodPatch, url, map[string]any{"default_branch": branch})
	return err
}

// httpError carries a non 2xx response.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.status, e.body)
}

// isNotFound reports whether err is a GitHub 404, unwrapping any context
// added along the way.
func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.status == http.StatusNotFound
}

// do performs a request and returns the body. The token never appears in
// the returned error.
func (p *Provider) do(ctx context.Context, method, url string, payload any) ([]byte, http.Header, error) {
	var reader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("github: encode request: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("github: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("github: read response: %w", err)
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
