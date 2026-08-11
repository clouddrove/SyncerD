package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func newProvider(t *testing.T, h http.Handler) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	p, err := New(Config{Name: "gh", Owner: "acme", APIURL: srv.URL, Token: "ghp_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return p, srv
}

func TestListReposPaginates(t *testing.T) {
	var page2URL string
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test_token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, []map[string]any{
				{"name": "second", "full_name": "acme/second", "default_branch": "main"},
			})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=2>; rel="next"`, page2URL))
		writeJSON(w, []map[string]any{
			{"name": "first", "full_name": "acme/first", "default_branch": "main", "archived": true},
		})
	})

	p, srv := newProvider(t, mux)
	page2URL = srv.URL + "/orgs/acme/repos"

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}
	if repos[0].Name != "first" || !repos[0].Archived {
		t.Errorf("repo 0 = %+v", repos[0])
	}
	if repos[0].Path != "acme/first" {
		t.Errorf("Path = %q", repos[0].Path)
	}
	if repos[1].Name != "second" {
		t.Errorf("repo 1 = %+v", repos[1])
	}
}

func TestListReposCarriesCloneURLFromAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"name": "first", "full_name": "acme/first", "default_branch": "main", "clone_url": "https://github.example/acme/first.git"},
		})
	})

	p, _ := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 || repos[0].CloneURL != "https://github.example/acme/first.git" {
		t.Fatalf("CloneURL not carried through: %+v", repos)
	}
}

func TestListReposFallsBackToComputedCloneURLWhenAPIOmitsIt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"name": "first", "full_name": "acme/first", "default_branch": "main"},
		})
	})

	p, srv := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := srv.URL + "/acme/first.git"
	if len(repos) != 1 || repos[0].CloneURL != want {
		t.Fatalf("computed fallback CloneURL = %+v, want %q", repos, want)
	}
}

func TestListReposFallsBackToUserEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/users/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"name": "personal", "full_name": "acme/personal", "default_branch": "main"}})
	})

	p, _ := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "personal" {
		t.Fatalf("unexpected repos: %+v", repos)
	}
}

func TestListReposSurfacesServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})

	p, _ := newProvider(t, mux)
	_, err := p.ListRepos(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry the status, got %v", err)
	}
	if strings.Contains(err.Error(), "ghp_test_token") {
		t.Errorf("error must not leak the token: %v", err)
	}
}

func TestEnsureRepoCreates(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/newrepo", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/orgs/acme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"login": "acme"})
	})
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"name": "newrepo", "full_name": "acme/newrepo"})
	})

	p, _ := newProvider(t, mux)
	got, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newrepo", Visibility: "private", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got.Name != "newrepo" {
		t.Errorf("Name = %q", got.Name)
	}
	if body["private"] != true {
		t.Errorf("private = %v, want true", body["private"])
	}
	if body["name"] != "newrepo" {
		t.Errorf("name = %v", body["name"])
	}
}

func TestEnsureRepoIsIdempotent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/existing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "existing", "full_name": "acme/existing"})
	})
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not create a repository that already exists")
	})

	p, _ := newProvider(t, mux)
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "existing"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func TestEnsureRepoCreatesUnderUserAccountWhenOwnerIsNotAnOrg(t *testing.T) {
	var createPath string
	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/newrepo", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/orgs/acme", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not create under the org endpoint when the owner is a personal account")
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		createPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"name": "newrepo", "full_name": "acme/newrepo"})
	})

	p, _ := newProvider(t, mux)
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newrepo", Visibility: "private"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if createPath != "/user/repos" {
		t.Errorf("createPath = %q, want /user/repos", createPath)
	}
	if body["name"] != "newrepo" {
		t.Errorf("name = %v", body["name"])
	}
}

func TestEnsureRepoDoesNotCreateOnNonNotFoundError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/thing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Server Error"}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a 500 on the existence check must be fatal, not a trigger to create")
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a 500 on the existence check must be fatal, not a trigger to create")
	})

	p, _ := newProvider(t, mux)
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "thing"}); err == nil {
		t.Fatal("expected the 500 to be surfaced")
	}
}

func TestListReposDoesNotFallBackOnNonNotFoundError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
	})
	mux.HandleFunc("/users/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a 403 on the org endpoint must not silently fall back to the user endpoint")
	})

	p, _ := newProvider(t, mux)
	if _, err := p.ListRepos(context.Background()); err == nil {
		t.Fatal("expected the 403 to be surfaced")
	}
}

func TestListReposDoesNotFallBackAfterPartialCollection(t *testing.T) {
	// Page one succeeds, page two 404s. That is a real failure, not a
	// signal that the owner is a personal account, so the already
	// collected repositories must not be silently discarded and refetched
	// from the user endpoint.
	var page2URL string

	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=2>; rel="next"`, page2URL))
		writeJSON(w, []map[string]any{
			{"name": "first", "full_name": "acme/first", "default_branch": "main"},
		})
	})
	mux.HandleFunc("/users/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a 404 partway through must not trigger the personal account fallback")
	})

	p, srv := newProvider(t, mux)
	page2URL = srv.URL + "/orgs/acme/repos"

	if _, err := p.ListRepos(context.Background()); err == nil {
		t.Fatal("a 404 on a later page must surface as an error")
	}
}

func TestIsNotFoundSeesThroughWrapping(t *testing.T) {
	base := &httpError{status: http.StatusNotFound, body: "Not Found"}
	wrapped := fmt.Errorf("github: determine whether %q is an organisation: %w", "acme", base)

	if !isNotFound(wrapped) {
		t.Error("isNotFound must unwrap; a bare type assertion would miss this")
	}
	if isNotFound(fmt.Errorf("plain error")) {
		t.Error("isNotFound must not match an unrelated error")
	}
}

func TestEnsureRepoIsConcurrencySafe(t *testing.T) {
	var mu sync.Mutex
	orgLookups := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		orgLookups++
		mu.Unlock()
		writeJSON(w, map[string]any{"login": "acme"})
	})
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"name": "created", "full_name": "acme/created"})
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})

	p, _ := newProvider(t, mux)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{
				Path: fmt.Sprintf("repo-%d", n), Visibility: "private",
			}); err != nil {
				t.Errorf("ensure %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if orgLookups != 1 {
		t.Errorf("owner kind resolved %d times, want 1", orgLookups)
	}
}

func TestCloneURLAndCredential(t *testing.T) {
	p, err := New(Config{Name: "gh", Owner: "acme", Token: "ghp_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("app"); got != "https://github.com/acme/app.git" {
		t.Errorf("CloneURL = %q", got)
	}

	cred, err := p.GitCredential(context.Background())
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.Kind != vcs.CredBasic {
		t.Errorf("Kind = %v, want basic", cred.Kind)
	}
	if cred.User != "x-access-token" {
		t.Errorf("User = %q", cred.User)
	}
	if cred.Secret != "ghp_test_token" {
		t.Errorf("Secret not passed through")
	}
	if p.SupportsNesting() {
		t.Error("GitHub repository names cannot contain a slash")
	}
}

func TestCloneURLForEnterprise(t *testing.T) {
	p, err := New(Config{Name: "ghes", Owner: "acme", APIURL: "https://ghe.example.com/api/v3", Token: "t"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("app"); got != "https://ghe.example.com/acme/app.git" {
		t.Errorf("CloneURL = %q", got)
	}
}

func TestNewRequiresOwner(t *testing.T) {
	if _, err := New(Config{Name: "gh", Token: "t"}); err == nil {
		t.Fatal("expected an error when owner is missing")
	}
}

func TestGitHubRejectsNestedNames(t *testing.T) {
	p, err := New(Config{Name: "gh", Owner: "acme", Token: "ghp_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p.SupportsNesting() {
		t.Error("GitHub repository names cannot contain a slash")
	}
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "team/app"}); err == nil {
		t.Error("EnsureRepo must reject a nested name rather than silently dropping a segment")
	}
	if err := p.SetDefaultBranch(context.Background(), "team/app", "main"); err == nil {
		t.Error("SetDefaultBranch must reject a nested name")
	}
}

func TestListReposRefusesToLoopOnRepeatedPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		// Always advertise a next link pointing back at this same URL,
		// which would loop forever without the visited set.
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, r.URL.String()))
		writeJSON(w, []map[string]any{
			{"name": "loop", "full_name": "acme/loop", "default_branch": "main"},
		})
	})

	p, _ := newProvider(t, mux)
	if _, err := p.ListRepos(context.Background()); err == nil {
		t.Fatal("a repeated next page must be refused rather than looped")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
