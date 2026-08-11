// Package gitlab implements the GitLab provider for git mirroring.
package gitlab

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

const defaultAPIURL = "https://gitlab.com"

// Config constructs a Provider.
type Config struct {
	Name       string
	Owner      string // group, may be group/subgroup
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

// Provider talks to the GitLab REST API and to GitLab over git.
//
// A Provider must not be copied after first use.
type Provider struct {
	name  string
	owner string
	base  string
	token string
	hc    *http.Client
}

// New validates cfg and returns a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Owner == "" {
		return nil, fmt.Errorf("gitlab provider %q: owner is required", cfg.Name)
	}
	base := strings.TrimRight(cfg.APIURL, "/")
	if base == "" {
		base = defaultAPIURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Provider{
		name:  cfg.Name,
		owner: strings.Trim(cfg.Owner, "/"),
		base:  base,
		token: cfg.Token,
		hc:    hc,
	}, nil
}

// Name returns the configured provider name.
func (p *Provider) Name() string { return p.name }

// Type returns the provider type string.
func (p *Provider) Type() string { return "gitlab" }

// SupportsNesting reports that GitLab supports nested group paths.
func (p *Provider) SupportsNesting() bool { return true }

// CloneURL returns the HTTPS git URL for a project path.
func (p *Provider) CloneURL(path string) string {
	if !strings.Contains(path, "/") {
		path = p.owner + "/" + path
	}
	return fmt.Sprintf("%s/%s.git", p.base, path)
}

// GitCredential returns the basic auth pair GitLab expects for HTTPS git.
func (p *Provider) GitCredential(context.Context) (vcs.GitCredential, error) {
	return vcs.GitCredential{Kind: vcs.CredBasic, User: "oauth2", Secret: p.token}, nil
}

// apiProject is the subset of the GitLab project object SyncerD reads.
type apiProject struct {
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	Archived          bool   `json:"archived"`
	EmptyRepo         bool   `json:"empty_repo"`
	ForkedFrom        *struct {
		ID int `json:"id"`
	} `json:"forked_from_project"`
}

func (a apiProject) toRepo(owner string) vcs.Repo {
	path := a.PathWithNamespace
	if path == "" {
		path = owner + "/" + a.Path
	}
	return vcs.Repo{
		Owner:         owner,
		Name:          a.Path,
		Path:          path,
		DefaultBranch: a.DefaultBranch,
		Archived:      a.Archived,
		Fork:          a.ForkedFrom != nil,
		Empty:         a.EmptyRepo,
	}
}

// maxPages bounds a paginated listing. GitLab caps at 100 per page, so this
// allows 100000 projects, far beyond any real group, while stopping a
// server that returns a cyclic X-Next-Page value.
const maxPages = 1000

// ListRepos returns every project under the configured group, including
// subgroups, following pagination via the X-Next-Page header. The walk is
// bounded by maxPages and tracks visited page tokens so a server that
// returns a cyclic or repeated page number cannot loop it forever; whatever
// was collected before a bound trip is still returned alongside the error.
func (p *Provider) ListRepos(ctx context.Context) ([]vcs.Repo, error) {
	var out []vcs.Repo
	page := "1"
	seen := make(map[string]bool)

	for page != "" {
		if seen[page] {
			return out, fmt.Errorf("gitlab: pagination revisited page %q, refusing to loop", page)
		}
		if len(seen) >= maxPages {
			return out, fmt.Errorf("gitlab: pagination exceeded %d pages, refusing to continue", maxPages)
		}
		seen[page] = true

		endpoint := fmt.Sprintf("%s/api/v4/groups/%s/projects?include_subgroups=true&per_page=100&archived=&page=%s",
			p.base, url.PathEscape(p.owner), url.QueryEscape(page))

		body, header, err := p.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return out, err
		}

		var projects []apiProject
		if err := json.Unmarshal(body, &projects); err != nil {
			return out, fmt.Errorf("gitlab: decode project list: %w", err)
		}
		for _, pr := range projects {
			out = append(out, pr.toRepo(p.owner))
		}

		page = strings.TrimSpace(header.Get("X-Next-Page"))
	}

	return out, nil
}

// EnsureRepo creates the project if it does not already exist.
func (p *Provider) EnsureRepo(ctx context.Context, spec vcs.RepoSpec) (vcs.Repo, error) {
	full := spec.Path
	if !strings.Contains(full, "/") {
		full = p.owner + "/" + full
	}

	getURL := fmt.Sprintf("%s/api/v4/projects/%s", p.base, url.PathEscape(full))
	body, _, err := p.do(ctx, http.MethodGet, getURL, nil)
	if err == nil {
		var existing apiProject
		if err := json.Unmarshal(body, &existing); err != nil {
			return vcs.Repo{}, fmt.Errorf("gitlab: decode project: %w", err)
		}
		return existing.toRepo(p.owner), nil
	}

	if !isNotFound(err) {
		return vcs.Repo{}, err
	}

	// Creating a project needs the numeric namespace id, not the path. This
	// is looked up fresh on every call rather than cached: unlike GitHub's
	// owner kind, a namespace id is only needed on the (rare) creation
	// path, not on every list or ensure call, so the extra request is not
	// worth the complexity of a guarded cache.
	namespace := full[:strings.LastIndex(full, "/")]
	nsID, err := p.namespaceID(ctx, namespace)
	if err != nil {
		return vcs.Repo{}, err
	}

	name := full[strings.LastIndex(full, "/")+1:]
	visibility := spec.Visibility
	if visibility == "" {
		visibility = "private"
	}

	payload := map[string]any{
		"name":         name,
		"path":         name,
		"namespace_id": nsID,
		"visibility":   visibility,
	}
	if spec.DefaultBranch != "" {
		payload["default_branch"] = spec.DefaultBranch
	}

	created, _, err := p.do(ctx, http.MethodPost, p.base+"/api/v4/projects", payload)
	if err != nil {
		return vcs.Repo{}, err
	}

	var out apiProject
	if err := json.Unmarshal(created, &out); err != nil {
		return vcs.Repo{}, fmt.Errorf("gitlab: decode created project: %w", err)
	}
	return out.toRepo(p.owner), nil
}

// namespaceID resolves a group path to its numeric id. Not cached; see the
// comment at its call site in EnsureRepo.
func (p *Provider) namespaceID(ctx context.Context, groupPath string) (int, error) {
	endpoint := fmt.Sprintf("%s/api/v4/groups/%s", p.base, url.PathEscape(groupPath))
	body, _, err := p.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("gitlab: look up group %q: %w", groupPath, err)
	}

	var group struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &group); err != nil {
		return 0, fmt.Errorf("gitlab: decode group: %w", err)
	}
	if group.ID == 0 {
		return 0, fmt.Errorf("gitlab: group %q has no id", groupPath)
	}
	return group.ID, nil
}

// SetDefaultBranch aligns the destination default branch with the source.
func (p *Provider) SetDefaultBranch(ctx context.Context, path, branch string) error {
	full := path
	if !strings.Contains(full, "/") {
		full = p.owner + "/" + full
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s", p.base, url.PathEscape(full))
	_, _, err := p.do(ctx, http.MethodPut, endpoint, map[string]any{"default_branch": branch})
	return err
}

// httpError carries a non 2xx response.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("gitlab: HTTP %d: %s", e.status, e.body)
}

// isNotFound reports whether err is a GitLab 404, unwrapping any context
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
			return nil, nil, fmt.Errorf("gitlab: encode request: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: build request: %w", err)
	}
	if p.token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: read response: %w", err)
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
