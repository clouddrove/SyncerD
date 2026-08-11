// Package bitbucket implements the Bitbucket Cloud provider for git
// mirroring.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

const defaultAPIURL = "https://api.bitbucket.org/2.0"

// Config constructs a Provider.
type Config struct {
	Name       string
	Owner      string // Bitbucket workspace
	APIURL     string
	Email      string // account email; app passwords were retired 2026-07-28
	Token      string // API token
	HTTPClient *http.Client
}

// Provider talks to the Bitbucket Cloud REST API and to bitbucket.org over
// git. Authentication is HTTP basic with the account email as the username
// and an API token as the password, for both the REST API and the git
// transport.
//
// Provider caches nothing per workspace lookup, so no mutex is needed: every
// call goes straight to the API. A future addition of a cached lookup must
// guard it, since the engine shares one Provider across worker goroutines
// and calls EnsureRepo concurrently.
type Provider struct {
	name      string
	owner     string // workspace
	apiURL    string
	cloneBase string
	email     string
	token     string
	hc        *http.Client
}

// New validates cfg and returns a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Owner == "" {
		return nil, fmt.Errorf("bitbucket provider %q: owner (workspace) is required", cfg.Name)
	}
	if cfg.Email == "" {
		return nil, fmt.Errorf("bitbucket provider %q: email is required; app passwords were retired on 2026-07-28 and API tokens authenticate with the account email", cfg.Name)
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
		email:     cfg.Email,
		token:     cfg.Token,
		hc:        hc,
	}, nil
}

// cloneBaseFor derives the web host used for git URLs from the API URL.
// Bitbucket Data Center deployments expose a different API host than the
// git web host, so a custom api_url yields a clone base built from that
// host rather than the fixed bitbucket.org default.
func cloneBaseFor(api string) string {
	if api == defaultAPIURL {
		return "https://bitbucket.org"
	}
	if u, err := url.Parse(api); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return api
}

// Name returns the configured provider name.
func (p *Provider) Name() string { return p.name }

// Type returns the provider type string.
func (p *Provider) Type() string { return "bitbucket" }

// SupportsNesting reports false. A Bitbucket repository slug cannot contain
// a slash: the only namespace is the workspace, which is configured
// separately.
func (p *Provider) SupportsNesting() bool { return false }

// CloneURL returns the HTTPS git URL for a repository name under the
// configured workspace. The name is relative to the workspace, matching
// what EnsureRepo creates.
//
// This is only used for a destination name; it assumes a bare name with no
// slash. A nested name is rejected upstream by SupportsNesting before the
// engine ever calls this method, so no guard is repeated here.
func (p *Provider) CloneURL(name string) string {
	return fmt.Sprintf("%s/%s/%s.git", p.cloneBase, p.owner, name)
}

// GitCredential returns the basic auth pair Bitbucket expects for HTTPS
// git: the account email as the username and the API token as the
// password. The same pair authenticates the REST API.
func (p *Provider) GitCredential(context.Context) (vcs.GitCredential, error) {
	return vcs.GitCredential{Kind: vcs.CredBasic, User: p.email, Secret: p.token}, nil
}

// apiRepo is the subset of the Bitbucket repository object SyncerD reads.
type apiRepo struct {
	Slug       string `json:"slug"`
	FullName   string `json:"full_name"`
	Mainbranch *struct {
		Name string `json:"name"`
	} `json:"mainbranch"` // null for an empty repository
	Parent *struct{} `json:"parent"` // present, non-null only when a fork
	Links  struct {
		Clone []struct {
			Name string `json:"name"`
			Href string `json:"href"`
		} `json:"clone"`
	} `json:"links"`
}

// toRepo converts the API representation to vcs.Repo. cloneBase is used
// only as a fallback to compose CloneURL when the API response omits an
// https clone link, since apiRepo has no access to the Provider that knows
// it.
func (a apiRepo) toRepo(owner, cloneBase string) vcs.Repo {
	path := a.FullName
	if path == "" {
		path = owner + "/" + a.Slug
	}

	cloneURL := ""
	for _, c := range a.Links.Clone {
		if c.Name == "https" {
			cloneURL = c.Href
			break
		}
	}
	if cloneURL == "" {
		cloneURL = fmt.Sprintf("%s/%s.git", cloneBase, path)
	}

	branch := ""
	if a.Mainbranch != nil {
		branch = a.Mainbranch.Name
	}

	return vcs.Repo{
		Owner:         owner,
		Name:          a.Slug,
		Path:          path,
		CloneURL:      cloneURL,
		DefaultBranch: branch,
		// Bitbucket Cloud has no archived concept for repositories, so this
		// is always false. skip_archived therefore has no effect on a
		// Bitbucket source, which is correct behaviour, not an oversight.
		Archived: false,
		Fork:     a.Parent != nil,
		Empty:    a.Mainbranch == nil,
	}
}

// reposPage is one page of a repository listing. Bitbucket carries the next
// page URL inside the JSON body as "next", unlike GitHub's Link header or
// GitLab's X-Next-Page header.
type reposPage struct {
	Values []apiRepo `json:"values"`
	Next   string    `json:"next"`
}

// maxPages bounds a paginated listing. Bitbucket caps pagelen at 100, so
// this allows 100000 repositories, far beyond any real workspace, while
// stopping a server that returns a cyclic next link.
const maxPages = 1000

// ListRepos returns every repository under the configured workspace,
// following the "next" URL embedded in each page's response body. The walk
// is bounded by maxPages and tracks visited page URLs so a server that
// returns a self referential or repeated next URL cannot loop it forever;
// whatever was collected before a bound trip is still returned alongside
// the error.
func (p *Provider) ListRepos(ctx context.Context) ([]vcs.Repo, error) {
	pageURL := fmt.Sprintf("%s/repositories/%s?pagelen=100", p.apiURL, p.owner)

	var out []vcs.Repo
	seen := make(map[string]bool)

	for pages := 0; pageURL != ""; pages++ {
		if pages >= maxPages {
			return out, fmt.Errorf("bitbucket: pagination exceeded %d pages, refusing to continue", maxPages)
		}
		if seen[pageURL] {
			return out, fmt.Errorf("bitbucket: pagination revisited a page, refusing to loop")
		}
		seen[pageURL] = true

		body, _, err := p.do(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return out, err
		}

		var page reposPage
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("bitbucket: decode repository list: %w", err)
		}
		for _, r := range page.Values {
			out = append(out, r.toRepo(p.owner, p.cloneBase))
		}

		pageURL = strings.TrimSpace(page.Next)
	}

	return out, nil
}

// EnsureRepo creates the repository if it does not already exist. The slug
// goes in the request path, not the body, which is where Bitbucket differs
// from both GitHub and GitLab.
func (p *Provider) EnsureRepo(ctx context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	name := spec.Path
	if strings.Contains(name, "/") {
		return vcs.Repo{}, fmt.Errorf("bitbucket: repository name %q contains a slash, which Bitbucket does not support; the workspace is configured separately", name)
	}

	repoURL := fmt.Sprintf("%s/repositories/%s/%s", p.apiURL, p.owner, name)
	body, _, err := p.do(ctx, http.MethodGet, repoURL, nil)
	if err == nil {
		var existing apiRepo
		if err := json.Unmarshal(body, &existing); err != nil {
			return vcs.Repo{}, fmt.Errorf("bitbucket: decode repository: %w", err)
		}
		return existing.toRepo(p.owner, p.cloneBase), nil
	}

	if !isNotFound(err) {
		return vcs.Repo{}, err
	}

	payload := map[string]any{
		"scm":        "git",
		"is_private": spec.Visibility != "public",
	}

	created, _, err := p.do(ctx, http.MethodPost, repoURL, payload)
	if err != nil {
		return vcs.Repo{}, err
	}

	var out apiRepo
	if err := json.Unmarshal(created, &out); err != nil {
		return vcs.Repo{}, fmt.Errorf("bitbucket: decode created repository: %w", err)
	}
	return out.toRepo(p.owner, p.cloneBase), nil
}

// SetDefaultBranch aligns the destination default branch with the source.
func (p *Provider) SetDefaultBranch(ctx context.Context, path, branch string) error {
	name := path
	if strings.Contains(name, "/") {
		return fmt.Errorf("bitbucket: repository name %q contains a slash, which Bitbucket does not support; the workspace is configured separately", name)
	}
	repoURL := fmt.Sprintf("%s/repositories/%s/%s", p.apiURL, p.owner, name)
	payload := map[string]any{
		"mainbranch": map[string]any{"name": branch, "type": "branch"},
	}
	_, _, err := p.do(ctx, http.MethodPut, repoURL, payload)
	return err
}

// httpError carries a non 2xx response.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("bitbucket: HTTP %d: %s", e.status, e.body)
}

// isNotFound reports whether err is a Bitbucket 404, unwrapping any context
// added along the way.
func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.status == http.StatusNotFound
}

// do performs a request and returns the body. The token never appears in
// the returned error.
func (p *Provider) do(ctx context.Context, method, endpoint string, payload any) ([]byte, http.Header, error) {
	var reader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("bitbucket: encode request: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("bitbucket: build request: %w", err)
	}
	req.SetBasicAuth(p.email, p.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("bitbucket: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("bitbucket: read response: %w", err)
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
