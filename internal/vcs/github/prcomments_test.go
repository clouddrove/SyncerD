package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/clouddrove/syncerd/internal/vcs"
)

func TestProviderImplementsPullRequestConversation(t *testing.T) {
	var _ vcs.PullRequestConversation = (*Provider)(nil)
}

func TestListCommentsPaginates(t *testing.T) {
	var page2URL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, []map[string]any{{"id": 2, "body": "second", "user": map[string]any{"login": "b"}}})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=2>; rel="next"`, page2URL))
		writeJSON(w, []map[string]any{{"id": 1, "body": "first", "user": map[string]any{"login": "a"},
			"created_at": "2026-08-02T09:00:00Z"}})
	})

	p, srv := newProvider(t, mux)
	page2URL = srv.URL + "/repos/acme/widget/issues/7/comments"

	got, err := p.ListComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments, want 2", len(got))
	}
	if got[0].SourceID != "1" || got[0].Body != "first" || got[0].Author.Handle != "a" {
		t.Errorf("comment 0 = %+v", got[0])
	}
	if got[0].CreatedAt.IsZero() {
		t.Error("CreatedAt was not decoded")
	}
}

func TestListReviewCommentsCarriesTheAnchor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"id": 5, "body": "nit", "user": map[string]any{"login": "rev"},
			"path": "internal/app.go", "line": 42, "side": "RIGHT", "commit_id": "abc123",
			"in_reply_to_id": 4,
		}})
	})
	mux.HandleFunc("/repos/acme/widget/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"number": 7, "base": map[string]any{"sha": "base123"}})
	})

	p, _ := newProvider(t, mux)
	got, err := p.ListReviewComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d review comments, want 1", len(got))
	}
	rc := got[0]
	if rc.Path != "internal/app.go" || rc.Line != 42 || rc.Side != "RIGHT" || rc.CommitSHA != "abc123" {
		t.Errorf("anchor lost: %+v", rc)
	}
	if rc.InReplyTo != "4" {
		t.Errorf("InReplyTo = %q, want 4", rc.InReplyTo)
	}
	// GitLab and CodeCommit cannot anchor a comment without it.
	if rc.BaseSHA != "base123" {
		t.Errorf("BaseSHA = %q, want base123", rc.BaseSHA)
	}
}

func TestListReviewCommentsSkipsTheBaseLookupWhenThereAreNone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{})
	})
	mux.HandleFunc("/repos/acme/widget/pulls/7", func(http.ResponseWriter, *http.Request) {
		t.Error("no inline comments means no reason to read the pull request")
	})

	p, _ := newProvider(t, mux)
	got, err := p.ListReviewComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %+v", got)
	}
}

func TestListReviewsSkipsPendingAndNormalisesState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 1, "state": "APPROVED", "user": map[string]any{"login": "m"}, "body": "lgtm"},
			{"id": 2, "state": "PENDING", "user": map[string]any{"login": "m"}, "body": "draft thoughts"},
			{"id": 3, "state": "CHANGES_REQUESTED", "user": map[string]any{"login": "n"}},
		})
	})

	p, _ := newProvider(t, mux)
	got, err := p.ListReviews(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a pending review is private to its author and must be skipped, got %d", len(got))
	}
	if got[0].State != "approved" || got[1].State != "changes_requested" {
		t.Errorf("states = %q, %q", got[0].State, got[1].State)
	}
}

func TestCreateAndUpdateAndDeleteComment(t *testing.T) {
	var created, updated map[string]any
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&created)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": 551})
	})
	mux.HandleFunc("/repos/acme/widget/issues/comments/551", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			_ = json.NewDecoder(r.Body).Decode(&updated)
			writeJSON(w, map[string]any{"id": 551})
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	})

	p, _ := newProvider(t, mux)
	id, err := p.CreateComment(context.Background(), "acme/widget", 7, "hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The id carries which space it belongs to: GitHub addresses
	// discussion and review comments through different routes.
	if id != "i:551" {
		t.Fatalf("id = %q, want i:551", id)
	}
	if created["body"] != "hello" {
		t.Errorf("created body = %v", created["body"])
	}

	if err := p.UpdateComment(context.Background(), "acme/widget", id, "edited"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated["body"] != "edited" {
		t.Errorf("updated body = %v", updated["body"])
	}

	if err := p.DeleteComment(context.Background(), "acme/widget", id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Error("delete did not reach the destination")
	}
}

func TestCreateReviewCommentSendsTheAnchor(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/12/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": 777})
	})

	p, _ := newProvider(t, mux)
	id, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment:   vcs.Comment{Body: "nit"},
		Path:      "internal/app.go",
		Line:      42,
		Side:      "RIGHT",
		CommitSHA: "abc123",
	})
	if err != nil {
		t.Fatalf("create review comment: %v", err)
	}
	if id != "p:777" {
		t.Errorf("id = %q, want the review comment space", id)
	}
	if payload["commit_id"] != "abc123" || payload["path"] != "internal/app.go" {
		t.Errorf("anchor not sent: %+v", payload)
	}
	if payload["line"] != float64(42) || payload["side"] != "RIGHT" {
		t.Errorf("line or side wrong: %+v", payload)
	}
}

func TestCreateReviewCommentReportsARejectedAnchor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/12/comments", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"line must be part of the diff"}`, http.StatusUnprocessableEntity)
	})

	p, _ := newProvider(t, mux)
	_, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "internal/app.go", Line: 42, CommitSHA: "abc123",
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("a 422 must surface as ErrAnchorRejected so the caller can downgrade, got %v", err)
	}
	if !strings.Contains(err.Error(), "internal/app.go:42") {
		t.Errorf("the error should name the anchor it could not place: %v", err)
	}
}

func TestConversationErrorsDoNotLeakTheToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})

	p, _ := newProvider(t, mux)
	_, err := p.ListComments(context.Background(), "acme/widget", 7)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "ghp_test_token") {
		t.Errorf("error must not leak the token: %v", err)
	}
}

func TestReviewCommentsAreUpdatedThroughTheirOwnRoute(t *testing.T) {
	// A review comment id comes from /pulls/{n}/comments and is not valid
	// in the /issues/comments space. Addressing it there 404s on every run
	// after the first, or edits an unrelated comment if the ids collide.
	var updatedVia, deletedVia string

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/pulls/comments/777", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			updatedVia = r.URL.Path
			writeJSON(w, map[string]any{"id": 777})
		case http.MethodDelete:
			deletedVia = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("/repos/acme/widget/issues/comments/777", func(http.ResponseWriter, *http.Request) {
		t.Error("a review comment must not be addressed through the issues route")
	})

	p, _ := newProvider(t, mux)
	if err := p.UpdateComment(context.Background(), "acme/widget", "p:777", "edited"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := p.DeleteComment(context.Background(), "acme/widget", "p:777"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if updatedVia == "" || deletedVia == "" {
		t.Error("the review comment route was never reached")
	}
}

func TestABareIDFromAnOlderStateFileIsADiscussionComment(t *testing.T) {
	// Before the id carried its space, every id was a bare number minted by
	// the issues route. An existing state file must keep working.
	reached := false
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widget/issues/comments/551", func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(w, map[string]any{"id": 551})
	})

	p, _ := newProvider(t, mux)
	if err := p.UpdateComment(context.Background(), "acme/widget", "551", "edited"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !reached {
		t.Error("a bare id must still resolve to the discussion route")
	}
}
