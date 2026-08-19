package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func TestProviderImplementsThePullRequestInterfaces(t *testing.T) {
	var _ vcs.PullRequestLister = (*Provider)(nil)
	var _ vcs.PullRequestWriter = (*Provider)(nil)
	var _ vcs.PullRequestReopener = (*Provider)(nil)
	var _ vcs.PullRequestConversation = (*Provider)(nil)
}

func TestListPullRequestsDecodesMergeRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "opened" {
			t.Errorf("state = %q, want opened", got)
		}
		writeJSON(w, []map[string]any{{
			"iid": 7, "title": "Add login", "description": "body", "state": "opened",
			"source_branch": "feature", "target_branch": "main", "sha": "aaa",
			"labels":  []string{"bug"},
			"author":  map[string]any{"username": "alice", "name": "Alice A"},
			"web_url": "https://gitlab.example/acme/widget/-/merge_requests/7",
		}})
	})

	p := newProvider(t, mux)
	prs, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d merge requests, want 1", len(prs))
	}
	pr := prs[0]
	if pr.Number != 7 || pr.HeadBranch != "feature" || pr.BaseBranch != "main" || pr.HeadSHA != "aaa" {
		t.Errorf("decoded wrong: %+v", pr)
	}
	if pr.State != vcs.PROpen || pr.Author.Handle != "alice" {
		t.Errorf("state or author wrong: %+v", pr)
	}
	if len(pr.Labels) != 1 || pr.Labels[0] != "bug" {
		t.Errorf("Labels = %v", pr.Labels)
	}
}

func TestMergedStateMapsToMerged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"iid": 7, "state": "merged", "merge_commit_sha": "deadbeef"}})
	})

	p := newProvider(t, mux)
	prs, _ := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if prs[0].State != vcs.PRMerged || prs[0].MergeSHA != "deadbeef" {
		t.Errorf("merged state lost: %+v", prs[0])
	}
}

func TestFindPullRequestFiltersBySourceBranch(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeJSON(w, []map[string]any{{"iid": 7, "state": "opened", "source_branch": "syncerd/pr/7"}})
	})

	p := newProvider(t, mux)
	got, ok, err := p.FindPullRequest(context.Background(), "acme/widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || got.Number != 7 {
		t.Fatalf("find = %+v, %v", got, ok)
	}
	if !strings.Contains(query, "source_branch=syncerd%2Fpr%2F7") {
		t.Errorf("query = %q", query)
	}
}

func TestFindPullRequestReportsAbsence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{})
	})

	p := newProvider(t, mux)
	if _, ok, err := p.FindPullRequest(context.Background(), "acme/widget", "syncerd/pr/7"); ok || err != nil {
		t.Fatalf("want absence with no error, got ok=%v err=%v", ok, err)
	}
}

func TestCreatePullRequestExpressesDraftThroughTheTitle(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"iid": 12, "state": "opened"})
	})

	p := newProvider(t, mux)
	got, err := p.CreatePullRequest(context.Background(), "acme/widget", vcs.PullRequestSpec{
		Title: "Add login", Body: "body", HeadBranch: "syncerd/pr/7", BaseBranch: "main",
		Draft: true, Labels: []string{"bug", "ui"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Number != 12 {
		t.Errorf("Number = %d", got.Number)
	}
	// GitLab has no writable draft field: the prefix is the only way.
	if payload["title"] != "Draft: Add login" {
		t.Errorf("title = %v, want the draft prefix applied", payload["title"])
	}
	if payload["source_branch"] != "syncerd/pr/7" || payload["target_branch"] != "main" {
		t.Errorf("branches wrong: %+v", payload)
	}
	if payload["labels"] != "bug,ui" {
		t.Errorf("labels = %v, want a comma separated string", payload["labels"])
	}
}

func TestUpdatePullRequestStripsTheDraftPrefixWhenNoLongerADraft(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/12", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"iid": 12})
	})

	p := newProvider(t, mux)
	err := p.UpdatePullRequest(context.Background(), "acme/widget", 12, vcs.PullRequestSpec{
		Title: "Draft: Add login", Body: "body", BaseBranch: "main", Draft: false,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if payload["title"] != "Add login" {
		t.Errorf("title = %v, want the draft prefix stripped", payload["title"])
	}
}

func TestStripDraftHandlesEveryRecognisedForm(t *testing.T) {
	for _, in := range []string{"Draft: X", "Draft:X", "[Draft] X", "[Draft]X", "(Draft) X", "(Draft)X", "X"} {
		if got := stripDraft(in); got != "X" {
			t.Errorf("stripDraft(%q) = %q, want X", in, got)
		}
	}
}

func TestCloseAndReopenUseStateEvents(t *testing.T) {
	var payloads []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/12", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		payloads = append(payloads, p)
		writeJSON(w, map[string]any{"iid": 12})
	})
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/12/merge", func(http.ResponseWriter, *http.Request) {
		t.Error("SyncerD must never merge at the destination")
	})

	p := newProvider(t, mux)
	if err := p.ClosePullRequest(context.Background(), "acme/widget", 12); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.ReopenPullRequest(context.Background(), "acme/widget", 12); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if payloads[0]["state_event"] != "close" || payloads[1]["state_event"] != "reopen" {
		t.Errorf("state events = %+v", payloads)
	}
}

func TestListCommentsSkipsSystemNotesAndInlineNotes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/7/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 1, "body": "a real comment", "author": map[string]any{"username": "bob"}},
			{"id": 2, "body": "changed the description", "system": true, "author": map[string]any{"username": "bob"}},
			{"id": 3, "body": "inline", "author": map[string]any{"username": "bob"},
				"position": map[string]any{"new_path": "a.go", "new_line": 4, "head_sha": "h", "base_sha": "b"}},
		})
	})

	p := newProvider(t, mux)
	got, err := p.ListComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Body != "a real comment" {
		t.Fatalf("a system note and an inline note must not appear as discussion: %+v", got)
	}
}

func TestListReviewCommentsCarriesTheSHAs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/7/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"id": 3, "body": "nit", "author": map[string]any{"username": "rev"},
			"position": map[string]any{"new_path": "internal/app.go", "new_line": 42, "head_sha": "head1", "base_sha": "base1"},
		}})
	})

	p := newProvider(t, mux)
	got, err := p.ListReviewComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d review comments, want 1", len(got))
	}
	rc := got[0]
	if rc.Path != "internal/app.go" || rc.Line != 42 || rc.CommitSHA != "head1" || rc.BaseSHA != "base1" {
		t.Errorf("anchor lost: %+v", rc)
	}
}

func TestListReviewsReportsApprovals(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/7/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"approved_by": []map[string]any{
			{"user": map[string]any{"id": 5, "username": "maintainer", "name": "M"}},
		}})
	})

	p := newProvider(t, mux)
	got, err := p.ListReviews(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].State != "approved" || got[0].Author.Handle != "maintainer" {
		t.Errorf("approvals = %+v", got)
	}
}

func TestCommentIDCarriesTheMergeRequestSoUpdatesCanAddressIt(t *testing.T) {
	var updated map[string]any
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/7/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 991})
	})
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/7/notes/991", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&updated)
			writeJSON(w, map[string]any{"id": 991})
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})

	p := newProvider(t, mux)
	id, err := p.CreateComment(context.Background(), "acme/widget", 7, "hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// GitLab addresses a note through its merge request, but the interface
	// hands back only an id, so the iid rides along inside it.
	if id != "7/991" {
		t.Fatalf("id = %q, want 7/991", id)
	}

	if err := p.UpdateComment(context.Background(), "acme/widget", id, "edited"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated["body"] != "edited" {
		t.Errorf("update body = %v", updated["body"])
	}
	if err := p.DeleteComment(context.Background(), "acme/widget", id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Error("delete did not reach the destination")
	}
}

func TestUpdateCommentRejectsAMalformedID(t *testing.T) {
	p := newProvider(t, http.NewServeMux())
	if err := p.UpdateComment(context.Background(), "acme/widget", "991", "x"); err == nil {
		t.Fatal("an id without the merge request iid cannot address a note")
	}
}

func TestCreateReviewCommentSendsTheFullPosition(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/12/discussions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"notes": []map[string]any{{"id": 777}}})
	})

	p := newProvider(t, mux)
	id, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "internal/app.go", Line: 42,
		Side: "RIGHT", CommitSHA: "head1", BaseSHA: "base1",
	})
	if err != nil {
		t.Fatalf("create review comment: %v", err)
	}
	if id != "12/777" {
		t.Errorf("id = %q", id)
	}
	pos, _ := payload["position"].(map[string]any)
	if pos["base_sha"] != "base1" || pos["head_sha"] != "head1" || pos["start_sha"] != "base1" {
		t.Errorf("GitLab requires all three SHAs: %+v", pos)
	}
	if pos["new_path"] != "internal/app.go" || pos["new_line"] != float64(42) {
		t.Errorf("path or line wrong: %+v", pos)
	}
}

func TestCreateReviewCommentWithoutSHAsIsARejectedAnchorNotAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/12/discussions", func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when the anchor cannot be built")
	})

	p := newProvider(t, mux)
	_, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "a.go", Line: 4,
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("a source without SHAs must downgrade, not fail: %v", err)
	}
}

func TestCreateReviewCommentTreatsA400AsARejectedAnchor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests/12/discussions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"line_code is invalid"}`, http.StatusBadRequest)
	})

	p := newProvider(t, mux)
	_, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "a.go", Line: 4, CommitSHA: "h", BaseSHA: "b",
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("a 400 on a position must downgrade, got %v", err)
	}
}

func TestPullRequestErrorsDoNotLeakTheToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fwidget/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	})

	p := newProvider(t, mux)
	_, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "glpat") {
		t.Errorf("error must not leak the token: %v", err)
	}
}
