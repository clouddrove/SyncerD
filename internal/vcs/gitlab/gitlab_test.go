package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func newProvider(t *testing.T, h http.Handler) *Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	p, err := New(Config{Name: "gl", Owner: "acme", APIURL: srv.URL, Token: "glpat_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return p
}

func TestListReposIncludesSubgroupsAndPaginates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "glpat_test_token" {
			t.Errorf("PRIVATE-TOKEN = %q", got)
		}
		if r.URL.Query().Get("include_subgroups") != "true" {
			t.Errorf("include_subgroups missing: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, []map[string]any{
				{"path": "second", "path_with_namespace": "acme/sub/second", "default_branch": "main"},
			})
			return
		}
		w.Header().Set("X-Next-Page", "2")
		writeJSON(w, []map[string]any{
			{"path": "first", "path_with_namespace": "acme/first", "default_branch": "main", "archived": true},
		})
	})

	p := newProvider(t, mux)
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
	if repos[1].Path != "acme/sub/second" {
		t.Errorf("subgroup path lost: %q", repos[1].Path)
	}
}

func TestListReposCarriesCloneURLFromAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"path": "first", "path_with_namespace": "acme/first", "default_branch": "main", "http_url_to_repo": "https://gitlab.example/acme/first.git"},
		})
	})

	p := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 || repos[0].CloneURL != "https://gitlab.example/acme/first.git" {
		t.Fatalf("CloneURL not carried through: %+v", repos)
	}
}

func TestListReposFallsBackToComputedCloneURLWhenAPIOmitsIt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"path": "first", "path_with_namespace": "acme/first", "default_branch": "main"},
		})
	})

	p := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := p.base + "/acme/first.git"
	if len(repos) != 1 || repos[0].CloneURL != want {
		t.Fatalf("computed fallback CloneURL = %+v, want %q", repos, want)
	}
}

func TestListReposFallsBackToUserNamespace(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404 Group Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/api/v4/users/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"path": "personal", "path_with_namespace": "acme/personal", "default_branch": "main"},
		})
	})

	p := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "personal" {
		t.Fatalf("unexpected repos: %+v", repos)
	}
}

func TestListReposDoesNotFallBackOnNonNotFoundError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	})
	mux.HandleFunc("/api/v4/users/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a 401 on the group endpoint must not silently fall back to the user namespace")
	})

	p := newProvider(t, mux)
	if _, err := p.ListRepos(context.Background()); err == nil {
		t.Fatal("expected the 401 to be surfaced")
	}
}

func TestEnsureRepoLooksUpNamespaceThenCreates(t *testing.T) {
	var created map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fnewproj", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/api/v4/groups/acme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 42, "full_path": "acme"})
	})
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&created)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"path": "newproj", "path_with_namespace": "acme/newproj"})
	})

	p := newProvider(t, mux)
	got, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newproj", Visibility: "private", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got.Path != "acme/newproj" {
		t.Errorf("Path = %q", got.Path)
	}
	if created["namespace_id"] != float64(42) {
		t.Errorf("namespace_id = %v, want 42", created["namespace_id"])
	}
	if created["visibility"] != "private" {
		t.Errorf("visibility = %v", created["visibility"])
	}
	if created["path"] != "newproj" {
		t.Errorf("path = %v", created["path"])
	}
}

func TestEnsureRepoIsIdempotent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fexisting", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"path": "existing", "path_with_namespace": "acme/existing"})
	})
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not create a project that already exists")
	})

	p := newProvider(t, mux)
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "existing"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func TestListReposSurfacesErrorWithoutLeakingToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	})

	p := newProvider(t, mux)
	_, err := p.ListRepos(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry the status: %v", err)
	}
	if strings.Contains(err.Error(), "glpat_test_token") {
		t.Errorf("error must not leak the token: %v", err)
	}
}

func TestCloneURLAndCredential(t *testing.T) {
	p, err := New(Config{Name: "gl", Owner: "acme", Token: "glpat_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("sub/app"); got != "https://gitlab.com/acme/sub/app.git" {
		t.Errorf("CloneURL = %q", got)
	}

	cred, err := p.GitCredential(context.Background())
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.Kind != vcs.CredBasic || cred.User != "oauth2" {
		t.Errorf("credential = %+v", cred)
	}
	if !p.SupportsNesting() {
		t.Error("GitLab supports nested group paths")
	}
}

func TestNewRequiresOwner(t *testing.T) {
	if _, err := New(Config{Name: "gl", Token: "t"}); err == nil {
		t.Fatal("expected an error when owner is missing")
	}
}

func TestEnsureRepoResolvesSubgroupBeneathOwner(t *testing.T) {
	var created map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fteam-a%2Fapp", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/api/v4/groups/acme%2Fteam-a", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 77, "full_path": "acme/team-a"})
	})
	mux.HandleFunc("/api/v4/groups/team-a", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a nested name must resolve beneath the configured owner, not against a top level group")
	})
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&created)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"path": "app", "path_with_namespace": "acme/team-a/app"})
	})

	p := newProvider(t, mux)
	got, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "team-a/app", Visibility: "private"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got.Path != "acme/team-a/app" {
		t.Errorf("Path = %q, want acme/team-a/app", got.Path)
	}
	if created["namespace_id"] != float64(77) {
		t.Errorf("namespace_id = %v, want 77", created["namespace_id"])
	}
}

func TestCloneURLIsRelativeToOwner(t *testing.T) {
	p, err := New(Config{Name: "gl", Owner: "acme", Token: "glpat_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("app"); got != "https://gitlab.com/acme/app.git" {
		t.Errorf("CloneURL(app) = %q", got)
	}
	if got := p.CloneURL("team-a/app"); got != "https://gitlab.com/acme/team-a/app.git" {
		t.Errorf("CloneURL(team-a/app) = %q", got)
	}
}

func TestListReposRefusesToLoopOnRepeatedPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		// Always advertise the same next page, which would loop forever
		// without the visited set.
		w.Header().Set("X-Next-Page", "2")
		writeJSON(w, []map[string]any{
			{"path": "loop", "path_with_namespace": "acme/loop", "default_branch": "main"},
		})
	})

	p := newProvider(t, mux)
	if _, err := p.ListRepos(context.Background()); err == nil {
		t.Fatal("a repeated next page must be refused rather than looped")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
