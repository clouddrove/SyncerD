package azuredevops

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

func TestListPullRequestsDecodesRefsAndFork(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("searchCriteria.status"); got != "active" {
			t.Errorf("status = %q, want active", got)
		}
		writeJSON(w, map[string]any{"value": []map[string]any{{
			"pullRequestId": 7, "title": "Add login", "description": "body", "status": "active",
			"sourceRefName": "refs/heads/feature", "targetRefName": "refs/heads/main",
			"createdBy":             map[string]any{"uniqueName": "alice@acme.com", "displayName": "Alice"},
			"lastMergeSourceCommit": map[string]any{"commitId": "aaa"},
			"labels":                []map[string]any{{"name": "bug"}},
			"forkSource": map[string]any{"repository": map[string]any{
				"remoteUrl": "https://acme@dev.azure.com/acme/team/_git/widget-fork",
			}},
		}}})
	})

	p, _ := newPATProvider(t, mux)
	prs, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d pull requests, want 1", len(prs))
	}
	pr := prs[0]
	// The full ref form is an Azure DevOps detail; the model carries plain
	// branch names.
	if pr.HeadBranch != "feature" || pr.BaseBranch != "main" {
		t.Errorf("refs not trimmed: %+v", pr)
	}
	if pr.HeadSHA != "aaa" || pr.State != vcs.PROpen {
		t.Errorf("decoded wrong: %+v", pr)
	}
	if !pr.IsFork() {
		t.Error("a forkSource means a fork head")
	}
	if strings.Contains(pr.HeadRepoCloneURL, "@") {
		t.Errorf("clone URL still carries userinfo: %q", pr.HeadRepoCloneURL)
	}
	if len(pr.Labels) != 1 || pr.Labels[0] != "bug" {
		t.Errorf("Labels = %v", pr.Labels)
	}
}

func TestStateMapping(t *testing.T) {
	cases := map[string]vcs.PRState{
		"active":    vcs.PROpen,
		"completed": vcs.PRMerged,
		"abandoned": vcs.PRClosed,
	}
	for in, want := range cases {
		if got := pullRequestState(apiPullRequest{Status: in}); got != want {
			t.Errorf("state(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindPullRequestSendsTheFullSourceRef(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("searchCriteria.sourceRefName")
		writeJSON(w, map[string]any{"value": []map[string]any{{"pullRequestId": 12, "status": "active"}}})
	})

	p, _ := newPATProvider(t, mux)
	got, ok, err := p.FindPullRequest(context.Background(), "widget", "syncerd/pr/7")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || got.Number != 12 {
		t.Fatalf("find = %+v, %v", got, ok)
	}
	if query != "refs/heads/syncerd/pr/7" {
		t.Errorf("sourceRefName = %q, want the full ref form", query)
	}
}

func TestCreatePullRequestSendsFullRefsAndLabels(t *testing.T) {
	var payload map[string]any
	var labelBody map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"pullRequestId": 12, "status": "active"})
	})
	// Labels live under the camel case route, unlike the pull request
	// route itself. Registering only this spelling proves the code uses it.
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/12/labels", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&labelBody)
		writeJSON(w, map[string]any{"name": "bug"})
	})

	p, _ := newPATProvider(t, mux)
	got, err := p.CreatePullRequest(context.Background(), "widget", vcs.PullRequestSpec{
		Title: "Add login", Body: "body", HeadBranch: "syncerd/pr/7", BaseBranch: "main",
		Draft: true, Labels: []string{"bug"}, SyncLabels: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Number != 12 {
		t.Errorf("Number = %d", got.Number)
	}
	if payload["sourceRefName"] != "refs/heads/syncerd/pr/7" || payload["targetRefName"] != "refs/heads/main" {
		t.Errorf("refs wrong: %+v", payload)
	}
	if payload["isDraft"] != true {
		t.Errorf("isDraft = %v", payload["isDraft"])
	}
	if labelBody["name"] != "bug" {
		t.Errorf("label not applied: %+v", labelBody)
	}
}

func TestUpdatePullRequestOmitsIsDraft(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests/12", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"pullRequestId": 12})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/12/labels", func(http.ResponseWriter, *http.Request) {
		t.Error("labels must not be touched when the mirror does not own them")
	})

	p, _ := newPATProvider(t, mux)
	if err := p.UpdatePullRequest(context.Background(), "widget", 12, vcs.PullRequestSpec{
		Title: "Add login", Body: "body", BaseBranch: "main", Draft: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// isDraft is not in the documented updatable set, so sending it risks a
	// silent ignore or an error.
	if _, ok := payload["isDraft"]; ok {
		t.Error("isDraft must not be patched")
	}
	if payload["title"] != "Add login" {
		t.Errorf("title = %v", payload["title"])
	}
}

func TestDescriptionIsTruncatedToTheDocumentedLimit(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := truncateDescription(long)
	if len(got) > 4000 {
		t.Fatalf("description is %d characters, over the 4000 limit", len(got))
	}
	if !strings.Contains(got, "truncated by SyncerD") {
		t.Error("a truncated description must say it was truncated")
	}
	if short := truncateDescription("fine"); short != "fine" {
		t.Errorf("a short description must be untouched, got %q", short)
	}
}

func TestCloseAndReopenPatchStatusAndNeverMerge(t *testing.T) {
	var payloads []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests/12", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		payloads = append(payloads, body)
		writeJSON(w, map[string]any{"pullRequestId": 12})
	})

	p, _ := newPATProvider(t, mux)
	if err := p.ClosePullRequest(context.Background(), "widget", 12); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.ReopenPullRequest(context.Background(), "widget", 12); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if payloads[0]["status"] != "abandoned" || payloads[1]["status"] != "active" {
		t.Errorf("statuses = %+v", payloads)
	}
	for _, body := range payloads {
		if _, ok := body["completionOptions"]; ok {
			t.Error("SyncerD must never complete (merge) a pull request at the destination")
		}
	}
}

func TestThreadsSplitIntoDiscussionAndInline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/7/threads", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []map[string]any{
			{"id": 100, "comments": []map[string]any{
				{"id": 1, "content": "a discussion comment", "commentType": "text",
					"author": map[string]any{"uniqueName": "bob@acme.com"}},
			}},
			{"id": 101, "threadContext": map[string]any{
				"filePath":       "/internal/app.go",
				"rightFileStart": map[string]any{"line": 42, "offset": 1},
			}, "comments": []map[string]any{
				{"id": 1, "content": "nit", "commentType": "text",
					"author": map[string]any{"uniqueName": "rev@acme.com"}},
			}},
			{"id": 102, "comments": []map[string]any{
				{"id": 1, "content": "alice voted", "commentType": "system"},
			}},
		}})
	})

	p, _ := newPATProvider(t, mux)

	discussion, err := p.ListComments(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(discussion) != 1 || discussion[0].Body != "a discussion comment" {
		t.Fatalf("a system thread must not be mirrored: %+v", discussion)
	}
	// The id has to carry pull request, thread, and comment, since Azure
	// needs all three to address it later.
	if discussion[0].SourceID != "7/100/1" {
		t.Errorf("SourceID = %q, want 7/100/1", discussion[0].SourceID)
	}

	inline, err := p.ListReviewComments(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list review comments: %v", err)
	}
	if len(inline) != 1 || inline[0].Path != "/internal/app.go" || inline[0].Line != 42 {
		t.Fatalf("inline anchor wrong: %+v", inline)
	}
}

func TestCreateReviewCommentSendsACharacterSpan(t *testing.T) {
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/12/threads", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		writeJSON(w, map[string]any{"id": 200, "comments": []map[string]any{{"id": 1}}})
	})

	p, _ := newPATProvider(t, mux)
	id, err := p.CreateReviewComment(context.Background(), "widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "/internal/app.go", Line: 42, Side: "RIGHT",
	})
	if err != nil {
		t.Fatalf("create review comment: %v", err)
	}
	if id != "12/200/1" {
		t.Errorf("id = %q, want 12/200/1", id)
	}

	tc, _ := payload["threadContext"].(map[string]any)
	start, _ := tc["rightFileStart"].(map[string]any)
	end, _ := tc["rightFileEnd"].(map[string]any)
	if tc["filePath"] != "/internal/app.go" {
		t.Errorf("filePath = %v", tc["filePath"])
	}
	// Azure DevOps anchors by character span, so a whole line runs to the
	// start of the next one.
	if start["line"] != float64(42) || end["line"] != float64(43) {
		t.Errorf("span = %+v to %+v", start, end)
	}
}

func TestCreateReviewCommentTreatsA400AsARejectedAnchor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/12/threads", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"The line is not part of the diff"}`, http.StatusBadRequest)
	})

	p, _ := newPATProvider(t, mux)
	_, err := p.CreateReviewComment(context.Background(), "widget", 12, vcs.ReviewComment{
		Comment: vcs.Comment{Body: "nit"}, Path: "/a.go", Line: 42,
	})
	if !errors.Is(err, vcs.ErrAnchorRejected) {
		t.Fatalf("a 400 on an anchor must downgrade, got %v", err)
	}
}

func TestCommentUpdateAndDeleteAddressThreadAndComment(t *testing.T) {
	var updated map[string]any
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/7/threads", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 100, "comments": []map[string]any{{"id": 1}}})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/7/threads/100/comments/1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			_ = json.NewDecoder(r.Body).Decode(&updated)
			writeJSON(w, map[string]any{"id": 1})
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})

	p, _ := newPATProvider(t, mux)
	id, err := p.CreateComment(context.Background(), "widget", 7, "hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "7/100/1" {
		t.Fatalf("id = %q", id)
	}
	if err := p.UpdateComment(context.Background(), "widget", id, "edited"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated["content"] != "edited" {
		t.Errorf("update body = %+v", updated)
	}
	if err := p.DeleteComment(context.Background(), "widget", id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Error("delete did not reach the destination")
	}
}

func TestSplitCommentIDRejectsTheOlderTwoPartForm(t *testing.T) {
	if _, _, _, err := splitCommentID("100/1"); err == nil {
		t.Fatal("a two part id cannot address an Azure DevOps comment")
	}
}

func TestListReviewsMapsVotesToVerdicts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/7/reviewers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []map[string]any{
			{"id": "g1", "uniqueName": "m@acme.com", "vote": 10},
			{"id": "g2", "uniqueName": "n@acme.com", "vote": -10},
			{"id": "g3", "uniqueName": "o@acme.com", "vote": 5},
			{"id": "g4", "uniqueName": "p@acme.com", "vote": 0},
		}})
	})

	p, _ := newPATProvider(t, mux)
	got, err := p.ListReviews(context.Background(), "widget", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("a zero vote is not a verdict: %+v", got)
	}
	if got[0].State != "approved" || got[1].State != "changes_requested" || got[2].State != "approved_with_suggestions" {
		t.Errorf("verdicts = %q, %q, %q", got[0].State, got[1].State, got[2].State)
	}
}

func TestPullRequestErrorsDoNotLeakTheToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
	})

	p, _ := newPATProvider(t, mux)
	_, err := p.ListPullRequests(context.Background(), "widget", vcs.PRListOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "pat-test-token") {
		t.Errorf("error must not leak the token: %v", err)
	}
}

func TestLabelsAreReconciledNotOnlyAdded(t *testing.T) {
	var added []string
	var removed []string

	mux := http.NewServeMux()
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullrequests/12", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"pullRequestId": 12})
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/12/labels", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"value": []map[string]any{{"name": "stale"}, {"name": "keep"}}})
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			added = append(added, body["name"].(string))
			writeJSON(w, body)
		}
	})
	mux.HandleFunc("/acme-org/acme-proj/_apis/git/repositories/widget/pullRequests/12/labels/stale", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			removed = append(removed, "stale")
			w.WriteHeader(http.StatusNoContent)
		}
	})

	p, _ := newPATProvider(t, mux)
	err := p.UpdatePullRequest(context.Background(), "widget", 12, vcs.PullRequestSpec{
		Title: "Add login", BaseBranch: "main",
		Labels: []string{"keep", "new"}, SyncLabels: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(added) != 1 || added[0] != "new" {
		t.Errorf("added = %v, want only the missing one", added)
	}
	if len(removed) != 1 || removed[0] != "stale" {
		t.Errorf("removed = %v, want the label the source dropped", removed)
	}
}

func TestTruncationCountsCharactersNotBytes(t *testing.T) {
	// 3000 CJK characters is well under the 4000 character limit but far
	// over it in bytes.
	body := strings.Repeat("字", 3000)
	if got := truncateDescription(body); got != body {
		t.Fatalf("a 3000 character body was truncated: %d runes in, %d out", 3000, len([]rune(got)))
	}

	long := strings.Repeat("字", 5000)
	got := truncateDescription(long)
	if n := len([]rune(got)); n > 4000 {
		t.Errorf("truncated to %d characters, over the limit", n)
	}
	if strings.ContainsRune(got, '�') {
		t.Error("truncation cut through a multi byte rune")
	}
}
