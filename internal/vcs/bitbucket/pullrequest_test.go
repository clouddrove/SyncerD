package bitbucket

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
	var _ vcs.PullRequestConversation = (*Provider)(nil)
}

func TestProviderDeliberatelyCannotReopen(t *testing.T) {
	// Bitbucket has no reopen endpoint. The engine branches on this exact
	// type assertion, so the absence is part of the contract and worth a
	// test rather than only a comment.
	if _, ok := any((*Provider)(nil)).(vcs.PullRequestReopener); ok {
		t.Fatal("Bitbucket cannot reopen a declined pull request and must not claim it can")
	}
}

func TestListPullRequestsDecodesForkAndSameRepoHeads(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "OPEN" {
			t.Errorf("state = %q, want OPEN", got)
		}
		writeJSON(w, map[string]any{"values": []map[string]any{
			{
				"id": 7, "title": "From a fork", "description": "please review", "state": "OPEN",
				"author": map[string]any{"nickname": "outsider", "display_name": "Out Sider"},
				"source": map[string]any{
					"branch": map[string]any{"name": "feature"},
					"commit": map[string]any{"hash": "aaa"},
					"repository": map[string]any{"full_name": "outsider/widget", "links": map[string]any{
						"clone": []map[string]any{{"name": "https", "href": "https://outsider@bitbucket.org/outsider/widget.git"}},
					}},
				},
				"destination": map[string]any{
					"branch":     map[string]any{"name": "main"},
					"repository": map[string]any{"full_name": "acme/widget"},
				},
				"links": map[string]any{"html": map[string]any{"href": "https://bitbucket.org/acme/widget/pull-requests/7"}},
			},
			{
				"id": 8, "title": "From a branch", "state": "OPEN", "draft": true,
				"source": map[string]any{
					"branch":     map[string]any{"name": "hotfix"},
					"commit":     map[string]any{"hash": "bbb"},
					"repository": map[string]any{"full_name": "acme/widget"},
				},
				"destination": map[string]any{
					"branch":     map[string]any{"name": "main"},
					"repository": map[string]any{"full_name": "acme/widget"},
				},
			},
		}})
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
		t.Error("pull request 7 comes from a fork")
	}
	// The clone URL Bitbucket reports carries userinfo, which the git
	// runner refuses; it must be stripped on the way through.
	if strings.Contains(fork.HeadRepoCloneURL, "@") {
		t.Errorf("clone URL still carries userinfo: %q", fork.HeadRepoCloneURL)
	}
	if fork.HeadSHA != "aaa" || fork.HeadBranch != "feature" || fork.BaseBranch != "main" {
		t.Errorf("decoded wrong: %+v", fork)
	}

	if prs[1].IsFork() {
		t.Error("pull request 8 lives in the source repository")
	}
	if !prs[1].Draft {
		t.Error("pull request 8 is a draft")
	}
}

func TestStateMapping(t *testing.T) {
	cases := map[string]vcs.PRState{
		"OPEN":       vcs.PROpen,
		"MERGED":     vcs.PRMerged,
		"DECLINED":   vcs.PRClosed,
		"SUPERSEDED": vcs.PRClosed,
	}
	for in, want := range cases {
		if got := pullRequestState(in); got != want {
			t.Errorf("pullRequestState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindPullRequestUsesTheQueryLanguage(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("q")
		writeJSON(w, map[string]any{"values": []map[string]any{{"id": 12, "state": "OPEN"}}})
	})

	p, _ := newProvider(t, mux)
	got, ok, err := p.FindPullRequest(context.Background(), "acme/widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || got.Number != 12 {
		t.Fatalf("find = %+v, %v", got, ok)
	}
	// Bitbucket has no source branch parameter; the filter goes through q.
	if want := `source.branch.name="syncerd/pr/7"`; query != want {
		t.Errorf("q = %q, want %q", query, want)
	}
}

func TestCreatePullRequestSendsTheNestedBranchShape(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"id": 12, "state": "OPEN"})
	})

	p, _ := newProvider(t, mux)
	got, err := p.CreatePullRequest(context.Background(), "acme/widget", vcs.PullRequestSpec{
		Title: "Add login", Body: "body", HeadBranch: "syncerd/pr/7", BaseBranch: "main",
		Draft: true, Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Number != 12 {
		t.Errorf("Number = %d", got.Number)
	}

	src, _ := payload["source"].(map[string]any)
	branch, _ := src["branch"].(map[string]any)
	if branch["name"] != "syncerd/pr/7" {
		t.Errorf("source branch wrong: %+v", payload)
	}
	if payload["draft"] != true {
		t.Errorf("draft = %v", payload["draft"])
	}
	// Bitbucket has no labels on a pull request, so they must not be sent.
	if _, ok := payload["labels"]; ok {
		t.Error("Bitbucket has no pull request labels; sending them would be a 400")
	}
}

func TestClosePullRequestDeclinesAndNeverMerges(t *testing.T) {
	declined := false
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests/12/decline", func(w http.ResponseWriter, r *http.Request) {
		declined = true
		writeJSON(w, map[string]any{"id": 12, "state": "DECLINED"})
	})
	mux.HandleFunc("/repositories/acme/widget/pullrequests/12/merge", func(http.ResponseWriter, *http.Request) {
		t.Error("SyncerD must never merge at the destination")
	})

	p, _ := newProvider(t, mux)
	if err := p.ClosePullRequest(context.Background(), "acme/widget", 12); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !declined {
		t.Error("close must go through decline")
	}
}

func TestListCommentsSeparatesInlineFromDiscussion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests/7/comments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"values": []map[string]any{
			{"id": 1, "content": map[string]any{"raw": "a discussion comment"}, "user": map[string]any{"nickname": "bob"}},
			{"id": 2, "content": map[string]any{"raw": "nit"}, "user": map[string]any{"nickname": "rev"},
				"inline": map[string]any{"path": "internal/app.go", "to": 42}},
			{"id": 3, "content": map[string]any{"raw": ""}, "deleted": true, "user": map[string]any{"nickname": "bob"}},
		}})
	})

	p, _ := newProvider(t, mux)

	discussion, err := p.ListComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(discussion) != 1 || discussion[0].Body != "a discussion comment" {
		t.Fatalf("discussion = %+v", discussion)
	}

	inline, err := p.ListReviewComments(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list review comments: %v", err)
	}
	if len(inline) != 1 {
		t.Fatalf("inline = %+v", inline)
	}
	if inline[0].Path != "internal/app.go" || inline[0].Line != 42 || inline[0].Side != "RIGHT" {
		t.Errorf("anchor wrong: %+v", inline[0])
	}
	// Bitbucket reports no commit SHA on an inline comment.
	if inline[0].CommitSHA != "" || inline[0].BaseSHA != "" {
		t.Errorf("Bitbucket has no anchor SHAs to report: %+v", inline[0])
	}
}

func TestListReviewsReadsParticipantVerdicts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"participants": []map[string]any{
			{"user": map[string]any{"nickname": "m", "uuid": "{1}"}, "state": "approved"},
			{"user": map[string]any{"nickname": "n", "uuid": "{2}"}, "state": "changes_requested"},
			{"user": map[string]any{"nickname": "o", "uuid": "{3}"}, "state": nil},
		}})
	})

	p, _ := newProvider(t, mux)
	got, err := p.ListReviews(context.Background(), "acme/widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a participant with no verdict must not appear: %+v", got)
	}
	if got[0].State != "approved" || got[1].State != "changes_requested" {
		t.Errorf("verdicts = %q, %q", got[0].State, got[1].State)
	}
}

func TestCommentIDCarriesThePullRequest(t *testing.T) {
	var updated map[string]any
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests/7/comments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 991})
	})
	mux.HandleFunc("/repositories/acme/widget/pullrequests/7/comments/991", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&updated)
			writeJSON(w, map[string]any{"id": 991})
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})

	p, _ := newProvider(t, mux)
	id, err := p.CreateComment(context.Background(), "acme/widget", 7, "hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "7/991" {
		t.Fatalf("id = %q, want 7/991", id)
	}

	if err := p.UpdateComment(context.Background(), "acme/widget", id, "edited"); err != nil {
		t.Fatalf("update: %v", err)
	}
	content, _ := updated["content"].(map[string]any)
	if content["raw"] != "edited" {
		t.Errorf("update body = %+v", updated)
	}
	if err := p.DeleteComment(context.Background(), "acme/widget", id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Error("delete did not reach the destination")
	}
}

func TestCreateReviewCommentAnchorsByPathAndLine(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests/12/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"id": 777})
	})

	p, _ := newProvider(t, mux)
	id, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "internal/app.go", Line: 42, Side: "RIGHT",
	})
	if err != nil {
		t.Fatalf("create review comment: %v", err)
	}
	if id != "12/777" {
		t.Errorf("id = %q", id)
	}
	inline, _ := payload["inline"].(map[string]any)
	if inline["path"] != "internal/app.go" || inline["to"] != float64(42) {
		t.Errorf("anchor wrong: %+v", inline)
	}
}

func TestCreateReviewCommentWithoutAPathIsARejectedAnchor(t *testing.T) {
	p, _ := newProvider(t, http.NewServeMux())
	_, err := p.CreateReviewComment(context.Background(), "acme/widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Line: 42,
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("want ErrAnchorRejected, got %v", err)
	}
}

func TestPullRequestErrorsDoNotLeakTheToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Unauthorized"}}`, http.StatusUnauthorized)
	})

	p, _ := newProvider(t, mux)
	_, err := p.ListPullRequests(context.Background(), "acme/widget", vcs.PRListOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "bb_test_token") {
		t.Errorf("error must not leak the token: %v", err)
	}
}

func TestUpdateCarriesTheDraftFlag(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/acme/widget/pullrequests/12", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"id": 12})
	})

	p, _ := newProvider(t, mux)
	if err := p.UpdatePullRequest(context.Background(), "acme/widget", 12, vcs.PullRequestSpec{
		Title: "Add login", BaseBranch: "main", Draft: false,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Omitting it would leave a pull request a draft at the destination
	// after it left draft at the source.
	if _, ok := payload["draft"]; !ok {
		t.Errorf("draft must be sent on update: %+v", payload)
	}
}
