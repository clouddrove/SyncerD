package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func TestProviderImplementsPullRequestWriter(t *testing.T) {
	var _ vcs.PullRequestWriter = (*Provider)(nil)
	var _ vcs.PullRequestReopener = (*Provider)(nil)
}

// mergeGuard registers handlers for every merge endpoint GitHub exposes and
// fails the test if any of them is reached. Merging at the destination would
// diverge from the mirrored base branch, so this is a rule worth enforcing
// in the tests rather than only in prose.
func mergeGuard(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	mux.HandleFunc("/repos/acme/widget/pulls/12/merge", func(http.ResponseWriter, *http.Request) {
		t.Error("SyncerD must never merge at the destination")
	})
	mux.HandleFunc("/repos/acme/widget/merges", func(http.ResponseWriter, *http.Request) {
		t.Error("SyncerD must never merge at the destination")
	})
}

func TestCreatePullRequestSendsTheFullSpec(t *testing.T) {
	var payload map[string]any
	var labelBody map[string]any

	mux := http.NewServeMux()
	mergeGuard(t, mux)
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{
			"number": 12, "state": "open",
			"head": map[string]any{"ref": "syncerd/pr/7", "sha": "aaa", "repo": map[string]any{"full_name": "acme/widget"}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}},
		})
	})
	mux.HandleFunc("/repos/acme/widget/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&labelBody)
		writeJSON(w, []map[string]any{{"name": "bug"}})
	})

	p, _ := newProvider(t, mux)
	got, err := p.CreatePullRequest(context.Background(), "acme/widget", vcs.PullRequestSpec{
		Title: "Add login", Body: "marker\n\nbody", HeadBranch: "syncerd/pr/7",
		BaseBranch: "main", Draft: true, Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Number != 12 {
		t.Errorf("Number = %d, want 12", got.Number)
	}
	if payload["head"] != "syncerd/pr/7" || payload["base"] != "main" {
		t.Errorf("head or base wrong: %+v", payload)
	}
	if payload["draft"] != true {
		t.Errorf("draft = %v, want true", payload["draft"])
	}
	if payload["title"] != "Add login" {
		t.Errorf("title = %v", payload["title"])
	}
	if labelBody == nil {
		t.Error("labels were not applied")
	}
}

func TestFindPullRequestMatchesOnHeadBranch(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeJSON(w, []map[string]any{{
			"number": 12, "state": "open",
			"head": map[string]any{"ref": "syncerd/pr/7", "sha": "aaa", "repo": map[string]any{"full_name": "acme/widget"}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}},
		}})
	})

	p, _ := newProvider(t, mux)
	got, ok, err := p.FindPullRequest(context.Background(), "acme/widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || got.Number != 12 {
		t.Fatalf("find = %+v, %v", got, ok)
	}
	if want := "head=acme%3Asyncerd%2Fpr%2F7"; !strings.Contains(query, want) {
		t.Errorf("query = %q, want it to carry %q", query, want)
	}
}

func TestFindPullRequestReportsAbsence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{})
	})

	p, _ := newProvider(t, mux)
	_, ok, err := p.FindPullRequest(context.Background(), "acme/widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if ok {
		t.Error("an empty result must report absence, not a zero pull request")
	}
}

func TestUpdatePullRequestPatchesMutableFields(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mergeGuard(t, mux)
	mux.HandleFunc("/repos/acme/widget/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"number": 12})
	})

	p, _ := newProvider(t, mux)
	err := p.UpdatePullRequest(context.Background(), "acme/widget", 12, vcs.PullRequestSpec{
		Title: "Add login v2", Body: "new body", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if payload["title"] != "Add login v2" || payload["body"] != "new body" {
		t.Errorf("payload = %+v", payload)
	}
	if _, ok := payload["head"]; ok {
		t.Error("the head branch must not be patched; GitHub does not allow it")
	}
}

func TestClosePullRequestClosesRatherThanMerging(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mergeGuard(t, mux)
	mux.HandleFunc("/repos/acme/widget/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"number": 12})
	})

	p, _ := newProvider(t, mux)
	if err := p.ClosePullRequest(context.Background(), "acme/widget", 12); err != nil {
		t.Fatalf("close: %v", err)
	}
	if payload["state"] != "closed" {
		t.Errorf("a merged source must close the destination, got state %v", payload["state"])
	}
}

func TestReopenPullRequest(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"number": 12})
	})

	p, _ := newProvider(t, mux)
	if err := p.ReopenPullRequest(context.Background(), "acme/widget", 12); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if payload["state"] != "open" {
		t.Errorf("state = %v, want open", payload["state"])
	}
}

func TestClearingLabelsSendsAnEmptyArrayNotNull(t *testing.T) {
	// A source pull request with no labels is the common case. A nil slice
	// marshals to null, which GitHub rejects with 422, and that failed the
	// whole pull request rather than just its labels.
	var raw map[string]json.RawMessage

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"number": 12})
	})
	mux.HandleFunc("/repos/acme/widget/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		writeJSON(w, []map[string]any{})
	})

	p, _ := newProvider(t, mux)
	err := p.UpdatePullRequest(context.Background(), "acme/widget", 12, vcs.PullRequestSpec{
		Title: "Add login", BaseBranch: "main", SyncLabels: true, Labels: nil,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := string(raw["labels"]); got != "[]" {
		t.Errorf("labels = %s, want an empty array", got)
	}
}
