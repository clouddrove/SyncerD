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
	if got := p.CloneURL("acme/sub/app"); got != "https://gitlab.com/acme/sub/app.git" {
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
