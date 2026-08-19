package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func TestProviderImplementsPullRequestLister(t *testing.T) {
	var _ vcs.PullRequestLister = (*Provider)(nil)
}

func TestListPullRequestsDecodesForkAndSameRepoHeads(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state = %q, want open", got)
		}
		writeJSON(w, []map[string]any{
			{
				"number": 7, "title": "From a fork", "body": "please review",
				"state": "open", "draft": false,
				"user":       map[string]any{"login": "outsider"},
				"head":       map[string]any{"ref": "feature", "sha": "aaa", "repo": map[string]any{"full_name": "outsider/widget", "clone_url": "https://github.com/outsider/widget.git"}},
				"base":       map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}},
				"labels":     []map[string]any{{"name": "bug"}},
				"html_url":   "https://github.com/acme/widget/pull/7",
				"created_at": "2026-08-01T10:00:00Z", "updated_at": "2026-08-02T10:00:00Z",
			},
			{
				"number": 8, "title": "From a branch", "state": "open", "draft": true,
				"user":       map[string]any{"login": "insider"},
				"head":       map[string]any{"ref": "hotfix", "sha": "bbb", "repo": map[string]any{"full_name": "acme/widget", "clone_url": "https://github.com/acme/widget.git"}},
				"base":       map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}},
				"created_at": "2026-08-03T10:00:00Z", "updated_at": "2026-08-03T10:00:00Z",
			},
		})
	})

	p, _ := newProvider(t, mux)
	prs, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d pull requests, want 2", len(prs))
	}

	fork := prs[0]
	if !fork.IsFork() {
		t.Error("pull request 7 is from a fork and must carry a head repository clone URL")
	}
	if fork.HeadRepoCloneURL != "https://github.com/outsider/widget.git" {
		t.Errorf("HeadRepoCloneURL = %q", fork.HeadRepoCloneURL)
	}
	if fork.HeadBranch != "feature" || fork.HeadSHA != "aaa" || fork.BaseBranch != "main" {
		t.Errorf("head or base wrong: %+v", fork)
	}
	if fork.State != vcs.PROpen || fork.Author.Handle != "outsider" {
		t.Errorf("state or author wrong: %+v", fork)
	}
	if len(fork.Labels) != 1 || fork.Labels[0] != "bug" {
		t.Errorf("Labels = %v", fork.Labels)
	}
	if fork.UpdatedAt.IsZero() {
		t.Error("UpdatedAt was not decoded")
	}
	if fork.WebURL != "https://github.com/acme/widget/pull/7" {
		t.Errorf("WebURL = %q", fork.WebURL)
	}

	same := prs[1]
	if same.IsFork() {
		t.Error("pull request 8 is from the source repository and must not be treated as a fork")
	}
	if !same.Draft {
		t.Error("pull request 8 is a draft")
	}
}

func TestListPullRequestsClearsTheHeadOfADeletedFork(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"number": 9, "state": "open",
			"head": map[string]any{"ref": "gone", "sha": "ccc", "repo": nil},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}},
		}})
	})

	p, _ := newProvider(t, mux)
	prs, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d pull requests, want 1", len(prs))
	}
	if prs[0].HeadSHA != "" {
		t.Errorf("a deleted fork has no reachable head, want an empty HeadSHA, got %q", prs[0].HeadSHA)
	}
	if prs[0].IsFork() {
		t.Error("a deleted fork must not be reported as a mirrorable fork head")
	}
}

func TestListPullRequestsMapsMergedState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"number": 10, "state": "closed", "merged_at": "2026-08-04T10:00:00Z",
			"head": map[string]any{"ref": "done", "sha": "ddd", "repo": map[string]any{"full_name": "acme/widget"}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}},
		}})
	})

	p, _ := newProvider(t, mux)
	prs, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if prs[0].State != vcs.PRMerged {
		t.Errorf("State = %q, want merged: GitHub reports a merged pull request as closed", prs[0].State)
	}
}

func TestListPullRequestsPaginates(t *testing.T) {
	var page2URL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, []map[string]any{{"number": 2, "state": "open",
				"head": map[string]any{"ref": "b", "sha": "b2", "repo": map[string]any{"full_name": "acme/widget"}},
				"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}}}})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=2>; rel="next"`, page2URL))
		writeJSON(w, []map[string]any{{"number": 1, "state": "open",
			"head": map[string]any{"ref": "a", "sha": "a1", "repo": map[string]any{"full_name": "acme/widget"}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widget"}}}})
	})

	p, srv := newProvider(t, mux)
	page2URL = srv.URL + "/repos/acme/widget/pulls"

	prs, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 2 || prs[0].Number != 1 || prs[1].Number != 2 {
		t.Fatalf("pagination wrong: %+v", prs)
	}
}

func TestListPullRequestsRejectsUnsupportedStates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unsupported state must be rejected before any request is made")
	})

	p, _ := newProvider(t, mux)
	_, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{States: []vcs.PRState{vcs.PRMerged}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestListPullRequestsDoesNotLeakTheToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})

	p, _ := newProvider(t, mux)
	_, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
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
