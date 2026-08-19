package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newPATProvider(t *testing.T, h http.Handler) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	p, err := New(Config{Name: "ado", Owner: "acme-org", Project: "acme-proj", APIURL: srv.URL, Token: "pat-test-token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return p, srv
}

func newEntraProvider(t *testing.T, h http.Handler, token string) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	p, err := New(Config{Name: "ado", Owner: "acme-org", Project: "acme-proj", APIURL: srv.URL, Auth: "entra", Token: token})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return p, srv
}

func TestListReposParsesValueArray(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"count": 2,
			"value": []map[string]any{
				{
					"id":            "11111111-1111-1111-1111-111111111111",
					"name":          "first",
					"defaultBranch": "refs/heads/main",
					"remoteUrl":     "https://myorg@dev.azure.com/myorg/myproject/_git/repo",
					"isDisabled":    true,
					"isFork":        true,
					"size":          4096,
				},
				{
					"id":         "22222222-2222-2222-2222-222222222222",
					"name":       "second",
					"remoteUrl":  "https://dev.azure.com/acme-org/acme-proj/_git/second",
					"isDisabled": false,
					"isFork":     false,
					"size":       0,
				},
			},
		})
	})

	p, _ := newPATProvider(t, mux)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}

	first := repos[0]
	if first.Name != "first" {
		t.Errorf("first.Name = %q, want %q", first.Name, "first")
	}
	if first.DefaultBranch != "main" {
		t.Errorf("first.DefaultBranch = %q, want %q (refs/heads/ prefix must be stripped)", first.DefaultBranch, "main")
	}
	if !first.Archived {
		t.Error("first.Archived = false, want true (isDisabled mapped to Archived)")
	}
	if !first.Fork {
		t.Error("first.Fork = false, want true (isFork mapped to Fork)")
	}
	if first.Empty {
		t.Error("first.Empty = true, want false (size != 0)")
	}
	if first.CloneURL != "https://dev.azure.com/myorg/myproject/_git/repo" {
		t.Errorf("first.CloneURL = %q, want remoteUrl value with userinfo stripped", first.CloneURL)
	}
	if strings.Contains(first.CloneURL, "@") {
		t.Errorf("first.CloneURL = %q, must not carry userinfo", first.CloneURL)
	}

	second := repos[1]
	if second.Archived {
		t.Error("second.Archived = true, want false")
	}
	if second.Fork {
		t.Error("second.Fork = true, want false")
	}
	if !second.Empty {
		t.Error("second.Empty = false, want true (size == 0)")
	}
}

func TestListReposNoDefaultBranchDoesNotPanic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"count": 1,
			"value": []map[string]any{
				{
					"id":        "33333333-3333-3333-3333-333333333333",
					"name":      "empty-repo",
					"remoteUrl": "https://dev.azure.com/acme-org/acme-proj/_git/empty-repo",
					"size":      0,
				},
			},
		})
	})

	p, _ := newPATProvider(t, mux)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(repos))
	}
	if repos[0].DefaultBranch != "" {
		t.Errorf("DefaultBranch = %q, want empty string", repos[0].DefaultBranch)
	}
	if !repos[0].Empty {
		t.Error("Empty = false, want true")
	}
}

// TestDefaultBranchRefRoundTrip pins that stripping the refs/heads/ prefix
// only removes a literal leading match: a branch name that itself contains
// a slash, such as "feature/x", keeps its internal slash, and a value that
// arrives already bare or empty passes through unchanged.
func TestDefaultBranchRefRoundTrip(t *testing.T) {
	cases := []struct {
		apiValue string
		want     string
	}{
		{"refs/heads/main", "main"},
		{"refs/heads/feature/x", "feature/x"},
		{"main", "main"},
		{"", ""},
	}

	for _, tc := range cases {
		mux := http.NewServeMux()
		mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"count": 1,
				"value": []map[string]any{
					{"id": "abc", "name": "repo", "defaultBranch": tc.apiValue, "remoteUrl": "https://example.invalid/repo", "size": 10},
				},
			})
		})

		p, _ := newPATProvider(t, mux)
		repos, err := p.ListRepos(context.Background())
		if err != nil {
			t.Fatalf("list %q: %v", tc.apiValue, err)
		}
		if len(repos) != 1 {
			t.Fatalf("list %q: got %d repos", tc.apiValue, len(repos))
		}
		if repos[0].DefaultBranch != tc.want {
			t.Errorf("defaultBranch %q stripped to %q, want %q", tc.apiValue, repos[0].DefaultBranch, tc.want)
		}
	}
}

func TestListReposFollowsContinuationToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("continuationToken") == "page2" {
			writeJSON(w, map[string]any{
				"count": 1,
				"value": []map[string]any{
					{"id": "2", "name": "second", "remoteUrl": "https://example/_git/second", "size": 1},
				},
			})
			return
		}
		w.Header().Set("x-ms-continuationtoken", "page2")
		writeJSON(w, map[string]any{
			"count": 1,
			"value": []map[string]any{
				{"id": "1", "name": "first", "remoteUrl": "https://example/_git/first", "size": 1},
			},
		})
	})

	p, _ := newPATProvider(t, mux)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}
	if repos[0].Name != "first" || repos[1].Name != "second" {
		t.Errorf("unexpected repos: %+v", repos)
	}
}

func TestListReposRefusesToLoopOnRepeatedContinuationToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-continuationtoken", "same-token")
		writeJSON(w, map[string]any{
			"count": 1,
			"value": []map[string]any{
				{"id": "1", "name": "loop", "remoteUrl": "https://example/_git/loop", "size": 1},
			},
		})
	})

	p, _ := newPATProvider(t, mux)

	_, err := p.ListRepos(context.Background())
	if err == nil {
		t.Fatal("expected an error from a repeated continuation token, got nil")
	}
	if !strings.Contains(err.Error(), "revisited") {
		t.Errorf("error = %v, want mention of revisiting a page", err)
	}
}

func TestPATModeSendsBasicAuthWithEmptyUsername(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		writeJSON(w, map[string]any{"count": 0, "value": []map[string]any{}})
	})

	p, _ := newPATProvider(t, mux)
	if _, err := p.ListRepos(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}

	if !gotOK {
		t.Fatal("request did not carry basic auth")
	}
	if gotUser != "" {
		t.Errorf("username = %q, want empty", gotUser)
	}
	if gotPass != "pat-test-token" {
		t.Errorf("password = %q, want the PAT", gotPass)
	}
}

func TestEntraModeSendsBearer(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeJSON(w, map[string]any{"count": 0, "value": []map[string]any{}})
	})

	p, _ := newEntraProvider(t, mux, "entra-access-token")
	if _, err := p.ListRepos(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}

	if gotAuth != "Bearer entra-access-token" {
		t.Errorf("Authorization = %q, want Bearer entra-access-token", gotAuth)
	}
}

func TestEntraModeWithNoTokenGitCredentialErrors(t *testing.T) {
	p, err := New(Config{Name: "ado", Owner: "acme-org", Project: "acme-proj", Auth: "entra"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, err = p.GitCredential(context.Background())
	if err == nil {
		t.Fatal("expected an error when entra mode has no token, got nil")
	}
}

func TestPATModeGitCredential(t *testing.T) {
	p, _ := newPATProvider(t, http.NewServeMux())

	cred, err := p.GitCredential(context.Background())
	if err != nil {
		t.Fatalf("gitcredential: %v", err)
	}
	if cred.Kind != vcs.CredBasic {
		t.Errorf("Kind = %v, want CredBasic", cred.Kind)
	}
	// Azure DevOps ignores the username and authenticates on the PAT alone,
	// but it cannot be empty: SyncerD's credential helper prints
	// "username=<value>", and git reads an empty value as no username,
	// then fails with "could not read Username" because prompts are off.
	if cred.User == "" {
		t.Error("User must not be empty; git cannot authenticate without one")
	}
	if cred.User != "acme-org" {
		t.Errorf("User = %q, want the organisation name", cred.User)
	}
	if cred.Secret != "pat-test-token" {
		t.Errorf("Secret = %q, want the PAT", cred.Secret)
	}
}

func TestEntraModeGitCredential(t *testing.T) {
	p, _ := newEntraProvider(t, http.NewServeMux(), "entra-access-token")

	cred, err := p.GitCredential(context.Background())
	if err != nil {
		t.Fatalf("gitcredential: %v", err)
	}
	if cred.Kind != vcs.CredBearer {
		t.Errorf("Kind = %v, want CredBearer", cred.Kind)
	}
	if cred.Secret != "entra-access-token" {
		t.Errorf("Secret = %q, want the entra token", cred.Secret)
	}
}

func TestEnsureRepoLooksUpProjectAndCreatesWhenAbsent(t *testing.T) {
	var projectLookups, creates int32
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/newrepo", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/acme-org/_apis/projects/acme-proj", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&projectLookups, 1)
		writeJSON(w, map[string]any{"id": "proj-guid-1"})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
			return
		}
		atomic.AddInt32(&creates, 1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "newrepo" {
			t.Errorf("create body name = %v, want newrepo", body["name"])
		}
		proj, _ := body["project"].(map[string]any)
		if proj["id"] != "proj-guid-1" {
			t.Errorf("create body project.id = %v, want proj-guid-1", proj["id"])
		}
		writeJSON(w, map[string]any{
			"id":        "repo-guid-1",
			"name":      "newrepo",
			"remoteUrl": "https://dev.azure.com/acme-org/acme-proj/_git/newrepo",
			"size":      0,
		})
	})

	p, _ := newPATProvider(t, mux)

	repo, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newrepo"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if repo.Name != "newrepo" {
		t.Errorf("repo.Name = %q, want newrepo", repo.Name)
	}
	if atomic.LoadInt32(&projectLookups) != 1 {
		t.Errorf("project lookups = %d, want 1", projectLookups)
	}
	if atomic.LoadInt32(&creates) != 1 {
		t.Errorf("creates = %d, want 1", creates)
	}
}

func TestEnsureRepoIdempotentWhenPresent(t *testing.T) {
	var projectLookups, creates int32
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/existing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id":            "repo-guid-2",
			"name":          "existing",
			"defaultBranch": "refs/heads/main",
			"remoteUrl":     "https://dev.azure.com/acme-org/acme-proj/_git/existing",
			"size":          123,
		})
	})
	mux.HandleFunc("/acme-org/_apis/projects/acme-proj", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&projectLookups, 1)
		writeJSON(w, map[string]any{"id": "proj-guid-1"})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&creates, 1)
		writeJSON(w, map[string]any{"id": "should-not-happen"})
	})

	p, _ := newPATProvider(t, mux)

	repo, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "existing"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if repo.Name != "existing" || repo.DefaultBranch != "main" {
		t.Errorf("unexpected repo: %+v", repo)
	}
	if atomic.LoadInt32(&projectLookups) != 0 {
		t.Errorf("project lookups = %d, want 0: an existing repository must not trigger a project lookup", projectLookups)
	}
	if atomic.LoadInt32(&creates) != 0 {
		t.Errorf("creates = %d, want 0", creates)
	}
}

func TestEnsureRepoDoesNotCreateOnNonNotFoundError(t *testing.T) {
	var creates int32
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server exploded", http.StatusInternalServerError)
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&creates, 1)
		writeJSON(w, map[string]any{"id": "should-not-happen"})
	})

	p, _ := newPATProvider(t, mux)

	_, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "broken"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if atomic.LoadInt32(&creates) != 0 {
		t.Errorf("creates = %d, want 0: a non 404 lookup failure must not fall through to create", creates)
	}
}

// TestProjectIDLookupOnceUnderConcurrentEnsureRepo asserts the project id
// cache is shared correctly: eight concurrent EnsureRepo calls for absent
// repositories must trigger exactly one project lookup. Run with -race.
func TestProjectIDLookupOnceUnderConcurrentEnsureRepo(t *testing.T) {
	var projectLookups int32
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/_apis/projects/acme-proj", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&projectLookups, 1)
		writeJSON(w, map[string]any{"id": "proj-guid-1"})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			writeJSON(w, map[string]any{"id": "repo-" + name, "name": name, "remoteUrl": "https://example/_git/" + name, "size": 0})
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	p, _ := newPATProvider(t, mux)

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: fmt.Sprintf("concurrent-%d", i)})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: ensure: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&projectLookups); got != 1 {
		t.Errorf("project lookups = %d, want exactly 1 across %d concurrent EnsureRepo calls", got, workers)
	}
}

// TestSetDefaultBranchSendsFullRefForNestedBranchName pins that a branch
// name containing a slash, such as "feature/x", is re-prefixed with
// refs/heads/ wholesale rather than having only its last segment turned
// into a ref, and that the PATCH still targets the repository GUID rather
// than the name.
// TestProjectIDLookupDoesNotCacheTransientFailure pins that a failed
// project lookup is not cached: projectIDCached only assigns the field
// after a successful decode, so a failure must not poison the cache for
// the life of the process. The first EnsureRepo hits a 500 on the project
// lookup and fails; the second must retry the lookup rather than reuse a
// cached error, and succeed.
func TestProjectIDLookupDoesNotCacheTransientFailure(t *testing.T) {
	var lookups int32
	var failFirst atomic.Bool
	failFirst.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/_apis/projects/acme-proj", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&lookups, 1)
		if failFirst.Swap(false) {
			http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"id": "11111111-2222-3333-4444-555555555555"})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/newrepo", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": "aaaa", "name": "newrepo", "remoteUrl": "https://example.invalid/newrepo"})
	})

	p, _ := newPATProvider(t, mux)

	// First attempt fails at the project lookup.
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newrepo"}); err == nil {
		t.Fatal("expected the first EnsureRepo to fail on the project lookup")
	}

	// Second attempt must retry the lookup rather than reuse a poisoned cache.
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newrepo"}); err != nil {
		t.Fatalf("second EnsureRepo should have retried the lookup and succeeded, got %v", err)
	}

	if got := atomic.LoadInt32(&lookups); got != 2 {
		t.Errorf("project lookups = %d, want 2: a failure must not be cached and a success must be", got)
	}
}

func TestSetDefaultBranchSendsFullRefForNestedBranchName(t *testing.T) {
	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/repo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": "guid-1234", "name": "repo", "remoteUrl": "https://example.invalid/repo"})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/guid-1234", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]any{"id": "guid-1234", "name": "repo"})
	})

	p, _ := newPATProvider(t, mux)
	if err := p.SetDefaultBranch(context.Background(), "repo", "feature/x"); err != nil {
		t.Fatalf("set default branch: %v", err)
	}
	if body["defaultBranch"] != "refs/heads/feature/x" {
		t.Errorf("defaultBranch = %v, want refs/heads/feature/x", body["defaultBranch"])
	}
}

func TestSetDefaultBranchSendsFullRefAndTargetsGUID(t *testing.T) {
	var patchedPath string
	var patchedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/target", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id":        "repo-guid-target",
			"name":      "target",
			"remoteUrl": "https://example/_git/target",
			"size":      1,
		})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/repo-guid-target", func(w http.ResponseWriter, r *http.Request) {
		patchedPath = r.URL.Path
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&patchedBody)
		w.WriteHeader(http.StatusOK)
	})

	p, _ := newPATProvider(t, mux)

	if err := p.SetDefaultBranch(context.Background(), "target", "develop"); err != nil {
		t.Fatalf("setdefaultbranch: %v", err)
	}
	if patchedPath != "/acme-org/acme-proj/_apis/git/repositories/repo-guid-target" {
		t.Errorf("patched path = %q, want the repository GUID path", patchedPath)
	}
	if patchedBody["defaultBranch"] != "refs/heads/develop" {
		t.Errorf("defaultBranch = %v, want refs/heads/develop", patchedBody["defaultBranch"])
	}
}

func Test401SurfacesStatusWithoutLeakingToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	p, _ := newPATProvider(t, mux)

	_, err := p.ListRepos(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want it to mention 401", err)
	}
	if strings.Contains(err.Error(), "pat-test-token") {
		t.Error("error leaks the raw token")
	}
	// The base64 form of ":pat-test-token" must not appear either.
	if strings.Contains(err.Error(), "OnBhdC10ZXN0LXRva2Vu") {
		t.Error("error leaks the base64 encoded basic auth header")
	}
}

func TestSupportsNestingIsFalse(t *testing.T) {
	p, _ := newPATProvider(t, http.NewServeMux())
	if p.SupportsNesting() {
		t.Error("SupportsNesting() = true, want false")
	}
}

func TestEnsureRepoRejectsNestedName(t *testing.T) {
	p, _ := newPATProvider(t, http.NewServeMux())

	_, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "group/repo"})
	if err == nil {
		t.Fatal("expected an error for a nested name, got nil")
	}
	if !strings.Contains(err.Error(), "slash") {
		t.Errorf("error = %v, want mention of a slash", err)
	}
}

func TestSetDefaultBranchRejectsNestedName(t *testing.T) {
	p, _ := newPATProvider(t, http.NewServeMux())

	err := p.SetDefaultBranch(context.Background(), "group/repo", "main")
	if err == nil {
		t.Fatal("expected an error for a nested name, got nil")
	}
	if !strings.Contains(err.Error(), "slash") {
		t.Errorf("error = %v, want mention of a slash", err)
	}
}

func TestCloneURL(t *testing.T) {
	p, srv := newPATProvider(t, http.NewServeMux())
	want := srv.URL + "/acme-org/acme-proj/_git/somerepo"
	if got := p.CloneURL("somerepo"); got != want {
		t.Errorf("CloneURL = %q, want %q", got, want)
	}
}
