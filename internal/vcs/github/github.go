// Package github implements the GitHub provider for git mirroring.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
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
type Provider struct {
	name      string
	owner     string
	apiURL    string
	cloneBase string
	token     string
	hc        *http.Client
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

// SupportsNesting reports that GitHub repository paths are owner/repo.
func (p *Provider) SupportsNesting() bool { return true }

// CloneURL returns the HTTPS git URL for a repository path.
func (p *Provider) CloneURL(path string) string {
	if !strings.Contains(path, "/") {
		path = p.owner + "/" + path
	}
	return fmt.Sprintf("%s/%s.git", p.cloneBase, path)
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
	url := fmt.Sprintf("%s/orgs/%s/repos?per_page=100&type=all", p.apiURL, p.owner)

	repos, err := p.listFrom(ctx, url)
	if err != nil {
		var he *httpError
		if errorsAs(err, &he) && he.status == http.StatusNotFound {
			userURL := fmt.Sprintf("%s/users/%s/repos?per_page=100&type=all", p.apiURL, p.owner)
			return p.listFrom(ctx, userURL)
		}
		return nil, err
	}
	return repos, nil
}

// listFrom walks the Link header chain starting at url.
func (p *Provider) listFrom(ctx context.Context, url string) ([]vcs.Repo, error) {
	var out []vcs.Repo

	for url != "" {
		body, header, err := p.do(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		var page []apiRepo
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("github: decode repository list: %w", err)
		}
		for _, r := range page {
			out = append(out, r.toRepo(p.owner))
		}

		url = nextLink(header.Get("Link"))
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

// EnsureRepo creates the repository if it does not already exist.
func (p *Provider) EnsureRepo(ctx context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	name := spec.Path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
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

	var he *httpError
	if !errorsAs(err, &he) || he.status != http.StatusNotFound {
		return vcs.Repo{}, err
	}

	payload := map[string]any{
		"name":    name,
		"private": spec.Visibility != "public",
	}
	createURL := fmt.Sprintf("%s/orgs/%s/repos", p.apiURL, p.owner)
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
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
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

// errorsAs is a tiny local wrapper so the file has one import fewer.
func errorsAs(err error, target **httpError) bool {
	he, ok := err.(*httpError)
	if ok {
		*target = he
	}
	return ok
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
