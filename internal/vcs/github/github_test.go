package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCloneURLAndCredential(t *testing.T) {
	p, err := New(Config{Name: "gh", Owner: "acme", Token: "ghp_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("acme/app"); got != "https://github.com/acme/app.git" {
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
	if !p.SupportsNesting() {
		t.Error("GitHub supports owner/repo paths")
	}
}

func TestCloneURLForEnterprise(t *testing.T) {
	p, err := New(Config{Name: "ghes", Owner: "acme", APIURL: "https://ghe.example.com/api/v3", Token: "t"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("acme/app"); got != "https://ghe.example.com/acme/app.git" {
		t.Errorf("CloneURL = %q", got)
	}
}

func TestNewRequiresOwner(t *testing.T) {
	if _, err := New(Config{Name: "gh", Token: "t"}); err == nil {
		t.Fatal("expected an error when owner is missing")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
