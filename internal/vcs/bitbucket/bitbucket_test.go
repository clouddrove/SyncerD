package bitbucket

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

	p, err := New(Config{Name: "bb", Owner: "acme", APIURL: srv.URL, Email: "svc@acme.com", Token: "bb_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return p, srv
}

func TestListReposPaginatesViaBodyNext(t *testing.T) {
	var page2URL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "svc@acme.com" || pass != "bb_test_token" {
			t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
		}
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, map[string]any{
				"values": []map[string]any{
					{"slug": "second", "full_name": "acme/second", "mainbranch": map[string]any{"name": "main"}},
				},
			})
			return
		}
		writeJSON(w, map[string]any{
			"next": page2URL,
			"values": []map[string]any{
				{"slug": "first", "full_name": "acme/first", "mainbranch": map[string]any{"name": "main"}},
			},
		})
	})

	p, srv := newProvider(t, mux)
	page2URL = srv.URL + "/repositories/acme?pagelen=100&page=2"

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

func TestListReposRefusesToLoopOnRepeatedNext(t *testing.T) {
	var selfURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		// Always advertise a next URL pointing back at this same page,
		// which would loop forever without the visited set.
		writeJSON(w, map[string]any{
			"next": selfURL,
			"values": []map[string]any{
				{"slug": "loop", "full_name": "acme/loop", "mainbranch": map[string]any{"name": "main"}},
			},
		})
	})

	p, srv := newProvider(t, mux)
	selfURL = srv.URL + "/repositories/acme?pagelen=100"

	if _, err := p.ListRepos(context.Background()); err == nil {
		t.Fatal("a repeated next page must be refused rather than looped")
	}
}

func TestListReposDetectsForkViaParent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"values": []map[string]any{
				{
					"slug":       "forked",
					"full_name":  "acme/forked",
					"mainbranch": map[string]any{"name": "main"},
					"parent":     map[string]any{"full_name": "upstream/forked"},
				},
			},
		})
	})

	p, _ := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 || !repos[0].Fork {
		t.Fatalf("fork not detected: %+v", repos)
	}
}

func TestListReposHandlesNullMainbranchOnEmptyRepo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"values": []map[string]any{
				{"slug": "empty", "full_name": "acme/empty", "mainbranch": nil},
			},
		})
	})

	p, _ := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(repos))
	}
	if !repos[0].Empty {
		t.Errorf("Empty = false, want true for a null mainbranch")
	}
	if repos[0].DefaultBranch != "" {
		t.Errorf("DefaultBranch = %q, want empty", repos[0].DefaultBranch)
	}
	if repos[0].Fork {
		t.Errorf("Fork = true, want false when parent is absent")
	}
}

func TestListReposTakesCloneURLFromHTTPSLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"values": []map[string]any{
				{
					"slug":       "first",
					"full_name":  "acme/first",
					"mainbranch": map[string]any{"name": "main"},
					"links": map[string]any{
						"clone": []map[string]any{
							{"name": "ssh", "href": "git@bitbucket.org:acme/first.git"},
							{"name": "https", "href": "https://bitbucket.org/acme/first.git"},
						},
					},
				},
			},
		})
	})

	p, _ := newProvider(t, mux)
	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 || repos[0].CloneURL != "https://bitbucket.org/acme/first.git" {
		t.Fatalf("CloneURL = %+v, want the https clone link", repos)
	}
}

func TestListReposFallsBackToComputedCloneURLWhenLinksOmitHTTPS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"values": []map[string]any{
				{"slug": "first", "full_name": "acme/first", "mainbranch": map[string]any{"name": "main"}},
			},
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

func TestEnsureRepoCreates(t *testing.T) {
	var body map[string]any
	var method string
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/newrepo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, `{"error":{"message":"Repository not found"}}`, http.StatusNotFound)
			return
		}
		method = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		writeJSON(w, map[string]any{"slug": "newrepo", "full_name": "acme/newrepo", "mainbranch": map[string]any{"name": "main"}})
	})

	p, _ := newProvider(t, mux)
	got, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "newrepo", Visibility: "private"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got.Name != "newrepo" {
		t.Errorf("Name = %q", got.Name)
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if body["scm"] != "git" {
		t.Errorf("scm = %v, want git", body["scm"])
	}
	if body["is_private"] != true {
		t.Errorf("is_private = %v, want true", body["is_private"])
	}
	if _, ok := body["slug"]; ok {
		t.Errorf("slug must not be sent in the body, it belongs in the path")
	}
}

func TestEnsureRepoIsIdempotent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/existing", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("must not create a repository that already exists")
			return
		}
		writeJSON(w, map[string]any{"slug": "existing", "full_name": "acme/existing", "mainbranch": map[string]any{"name": "main"}})
	})

	p, _ := newProvider(t, mux)
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "existing"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func TestEnsureRepoDoesNotCreateOnNonNotFoundError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/thing", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("a 500 on the existence check must be fatal, not a trigger to create")
			return
		}
		http.Error(w, `{"error":{"message":"Internal Server Error"}}`, http.StatusInternalServerError)
	})

	p, _ := newProvider(t, mux)
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "thing"}); err == nil {
		t.Fatal("expected the 500 to be surfaced")
	}
}

func TestUnauthorizedSurfacesStatusWithoutLeakingToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Invalid credentials"}}`, http.StatusUnauthorized)
	})

	p, _ := newProvider(t, mux)
	_, err := p.ListRepos(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry the status, got %v", err)
	}
	if strings.Contains(err.Error(), "bb_test_token") {
		t.Errorf("error must not leak the token: %v", err)
	}
}

func TestIsNotFoundSeesThroughWrapping(t *testing.T) {
	base := &httpError{status: http.StatusNotFound, body: "Not Found"}
	wrapped := fmt.Errorf("bitbucket: look up repository %q: %w", "acme/thing", base)

	if !isNotFound(wrapped) {
		t.Error("isNotFound must unwrap; a bare type assertion would miss this")
	}
	if isNotFound(fmt.Errorf("plain error")) {
		t.Error("isNotFound must not match an unrelated error")
	}
}

func TestGitCredentialUsesEmailAsUsername(t *testing.T) {
	p, err := New(Config{Name: "bb", Owner: "acme", Email: "svc@acme.com", Token: "bb_test_token"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	cred, err := p.GitCredential(context.Background())
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.Kind != vcs.CredBasic {
		t.Errorf("Kind = %v, want basic", cred.Kind)
	}
	if cred.User != "svc@acme.com" {
		t.Errorf("User = %q, want the configured email, not the bitbucket username", cred.User)
	}
	if cred.Secret != "bb_test_token" {
		t.Errorf("Secret not passed through")
	}
}

func TestNewRequiresOwnerAndEmail(t *testing.T) {
	if _, err := New(Config{Name: "bb", Email: "svc@acme.com", Token: "t"}); err == nil {
		t.Fatal("expected an error when owner is missing")
	}
	if _, err := New(Config{Name: "bb", Owner: "acme", Token: "t"}); err == nil {
		t.Fatal("expected an error when email is missing")
	}
}

func TestSupportsNestingFalseAndRejectsNestedNames(t *testing.T) {
	p, err := New(Config{Name: "bb", Owner: "acme", Email: "svc@acme.com", Token: "t"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p.SupportsNesting() {
		t.Error("Bitbucket repository slugs cannot contain a slash")
	}
	if _, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "team/app"}); err == nil {
		t.Error("EnsureRepo must reject a nested name rather than silently dropping a segment")
	}
	if err := p.SetDefaultBranch(context.Background(), "team/app", "main"); err == nil {
		t.Error("SetDefaultBranch must reject a nested name")
	}
}

func TestCloneURL(t *testing.T) {
	p, err := New(Config{Name: "bb", Owner: "acme", Email: "svc@acme.com", Token: "t"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("app"); got != "https://bitbucket.org/acme/app.git" {
		t.Errorf("CloneURL = %q", got)
	}
}

func TestCloneURLForAPIURLOverride(t *testing.T) {
	p, err := New(Config{Name: "bb-dc", Owner: "acme", Email: "svc@acme.com", Token: "t", APIURL: "https://bitbucket.example.com/rest/api"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.CloneURL("app"); got != "https://bitbucket.example.com/acme/app.git" {
		t.Errorf("CloneURL = %q, want the derived host from api_url", got)
	}
}

func TestSetDefaultBranchSendsMainbranch(t *testing.T) {
	var body map[string]any
	var method string
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/thing", func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]any{"slug": "thing", "full_name": "acme/thing"})
	})

	p, _ := newProvider(t, mux)
	if err := p.SetDefaultBranch(context.Background(), "thing", "develop"); err != nil {
		t.Fatalf("set default branch: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %q, want PUT", method)
	}
	mb, ok := body["mainbranch"].(map[string]any)
	if !ok {
		t.Fatalf("mainbranch missing from body: %+v", body)
	}
	if mb["name"] != "develop" || mb["type"] != "branch" {
		t.Errorf("mainbranch = %+v", mb)
	}
}

func TestEnsureRepoIsConcurrencySafe(t *testing.T) {
	var mu sync.Mutex
	created := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, `{"error":{"message":"Repository not found"}}`, http.StatusNotFound)
			return
		}
		mu.Lock()
		created++
		mu.Unlock()
		writeJSON(w, map[string]any{"slug": "created", "full_name": "acme/created", "mainbranch": map[string]any{"name": "main"}})
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
	if created != 8 {
		t.Errorf("created %d repos, want 8", created)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
