//go:build live

// Package livetest runs SyncerD against a real provider.
//
// Everything else in this repository is proven against fakes the same
// author wrote, and that has already been shown to be insufficient: three
// defects that made pull request mirroring inoperable survived a green test
// suite, a passing verification gate, and a review of their own change,
// because every fake agreed with the mistake. A real API disagrees.
//
// These tests are behind the "live" build tag and skip unless credentials
// are supplied, so an ordinary `go test ./...` never reaches them:
//
//	SYNCERD_LIVE_GITHUB_TOKEN=ghp_...  a token with repo scope
//	SYNCERD_LIVE_GITHUB_OWNER=acme     the account or org to create under
//	go test -tags live ./internal/livetest/ -v -timeout 15m
//
// They create two throwaway repositories, use them, and delete them. The
// names carry a fixed prefix and a timestamp so a failed cleanup is
// obvious and easy to sweep up by hand.
package livetest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/gitsync"
	"github.com/clouddrove/syncerd/internal/prsync"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
	"github.com/clouddrove/syncerd/internal/vcs/github"
)

const repoPrefix = "syncerd-live-"

// env reports a required environment value, skipping the test when it is
// absent so the suite is inert without credentials.
func env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s is not set; live tests need real credentials", key)
	}
	return v
}

// liveProvider builds a GitHub provider from the environment.
func liveProvider(t *testing.T) (*github.Provider, string) {
	t.Helper()
	token := env(t, "SYNCERD_LIVE_GITHUB_TOKEN")
	owner := env(t, "SYNCERD_LIVE_GITHUB_OWNER")

	p, err := github.New(github.Config{Name: "live", Owner: owner, Token: token})
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	return p, owner
}

// createRepo makes a throwaway repository and registers its deletion.
func createRepo(t *testing.T, p *github.Provider, name string) vcs.Repo {
	t.Helper()
	ctx := context.Background()

	repo, err := p.EnsureRepo(ctx, vcs.RepoSpec{Path: name, Visibility: "private", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		if os.Getenv("SYNCERD_LIVE_KEEP") != "" {
			t.Logf("keeping %s at your request", name)
			return
		}
		if err := deleteRepo(p, name); err != nil {
			t.Errorf("could not delete %s, remove it by hand: %v", name, err)
		}
	})
	return repo
}

// deleteRepo removes a throwaway repository through the API the provider
// already authenticates against.
func deleteRepo(p *github.Provider, name string) error {
	// Deliberately not a provider method: SyncerD must never be able to
	// delete a repository in ordinary operation, so the capability lives
	// here in the tests that need it and nowhere else.
	owner := os.Getenv("SYNCERD_LIVE_GITHUB_OWNER")
	token := os.Getenv("SYNCERD_LIVE_GITHUB_TOKEN")
	cmd := exec.Command("gh", "api", "-X", "DELETE", fmt.Sprintf("repos/%s/%s", owner, name))
	cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// git runs a git command in dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// seedRepo pushes an initial commit and a feature branch to a repository,
// using the token in the remote URL for this one throwaway case.
func seedRepo(t *testing.T, owner, name, token string) {
	t.Helper()
	work := t.TempDir()
	remote := fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", token, owner, name)

	git(t, work, "init", "-q", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("live test\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "-c", "user.email=live@example.com", "-c", "user.name=live", "commit", "-q", "-m", "initial")
	git(t, work, "push", "-q", remote, "main")

	// A branch with a change, which becomes the pull request.
	git(t, work, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("a change\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "-c", "user.email=live@example.com", "-c", "user.name=live", "commit", "-q", "-m", "add a feature")
	git(t, work, "push", "-q", remote, "feature")
}

// openPullRequest opens one at the source and returns its number.
func openPullRequest(t *testing.T, p *github.Provider, repoPath string) int {
	t.Helper()
	pr, err := p.CreatePullRequest(context.Background(), repoPath, vcs.PullRequestSpec{
		Title:      "A live pull request",
		Body:       "Opened by the live test. Mentions @nobody and #1 to prove they are neutralised.",
		HeadBranch: "feature",
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("open pull request: %v", err)
	}
	return pr.Number
}

// relist reads the source pull requests again, as each run does, and
// asserts how many are open.
func relist(t *testing.T, p *github.Provider, repoPath string, want int) []vcs.PullRequest {
	t.Helper()
	prs, err := p.ListPullRequests(context.Background(), repoPath, vcs.PRListOptions{})
	if err != nil {
		t.Fatalf("list pull requests: %v", err)
	}
	if len(prs) != want {
		t.Fatalf("got %d open pull requests, want %d", len(prs), want)
	}
	return prs
}

// TestGitHubToGitHubPullRequestRoundTrip mirrors a repository and its open
// pull request to a second repository, against the real API.
//
// This is the test that would have caught the three defects that shipped:
// the destination repository path being owner relative rather than
// qualified, the comment id spaces being addressed through one route, and
// a merged source leaving its mirror open forever.
func TestGitHubToGitHubPullRequestRoundTrip(t *testing.T) {
	p, owner := liveProvider(t)
	token := os.Getenv("SYNCERD_LIVE_GITHUB_TOKEN")
	ctx := context.Background()

	stamp := time.Now().UTC().Format("20060102-150405")
	srcName := repoPrefix + "src-" + stamp
	dstName := repoPrefix + "dst-" + stamp

	createRepo(t, p, srcName)
	createRepo(t, p, dstName)
	seedRepo(t, owner, srcName, token)

	srcPath := owner + "/" + srcName
	dstPath := owner + "/" + dstName
	number := openPullRequest(t, p, srcPath)
	t.Logf("source pull request %s#%d", srcPath, number)

	// Read it back the way a run would.
	prs := relist(t, p, srcPath, 1)

	// Mirror the branches first: a destination pull request cannot
	// reference commits that have not arrived.
	runner := gitsync.NewRunner(gitsync.NewRedactor(token))
	cache := filepath.Join(t.TempDir(), "cache.git")
	cred, err := p.GitCredential(ctx)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if err := runner.SyncCache(ctx, p.CloneURL(srcName), cache, cred); err != nil {
		t.Fatalf("sync cache: %v", err)
	}
	if err := runner.FetchPRHead(ctx, cache, p.CloneURL(srcName), "refs/heads/feature", "syncerd/pr/"+fmt.Sprint(number), cred); err != nil {
		t.Fatalf("fetch pull request head: %v", err)
	}
	if _, err := runner.Push(ctx, cache, p.CloneURL(dstName), cred, gitsync.PushMirror, false); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Now the objects, through the same path the engine uses.
	st := state.NewGit()
	res, err := prsync.Sync(ctx, prs, prsync.Options{
		Mirror:       "live",
		SourceRepo:   srcPath,
		DestRepo:     p.QualifiedPath(dstName),
		BranchPrefix: "syncerd/pr",
		Source:       p,
		Dest:         p,
		SourceConv:   p,
		DestConv:     p,
		Comments:     true,
		Reviews:      true,
		Labels:       true,
		State:        st,
	})
	if err != nil {
		t.Fatalf("pull request sync: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("Created = %d, want 1", res.Created)
	}

	// The destination pull request must exist and be findable by the head
	// branch, which is how a lost state file recovers.
	found, ok, err := p.FindPullRequest(ctx, dstPath, "syncerd/pr/"+fmt.Sprint(number))
	if err != nil {
		t.Fatalf("find destination pull request: %v", err)
	}
	if !ok {
		t.Fatal("the destination pull request was not created")
	}
	t.Logf("destination pull request %s#%d", dstPath, found.Number)

	if !strings.Contains(found.Body, prsync.Marker(srcPath, number)) {
		t.Error("the mirrored body carries no marker, so a later run cannot recognise its own write")
	}
	if strings.Contains(found.Body, "@nobody") {
		t.Error("a mention survived into the destination and would notify a real account")
	}

	// A second run must change nothing.
	prs = relist(t, p, srcPath, 1)
	res, err = prsync.Sync(ctx, prs, prsync.Options{
		Mirror: "live", SourceRepo: srcPath, DestRepo: p.QualifiedPath(dstName),
		BranchPrefix: "syncerd/pr", Source: p, Dest: p, SourceConv: p, DestConv: p,
		Comments: true, Reviews: true, Labels: true, State: st,
	})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.Created != 0 {
		t.Errorf("a second run created %d pull requests; it must create none", res.Created)
	}

	// A comment at the source must reach the destination, exactly once.
	commentID, err := p.CreateComment(ctx, srcPath, number, "a comment from the live test")
	if err != nil {
		t.Fatalf("comment at the source: %v", err)
	}
	t.Logf("source comment %s", commentID)

	// Re-list, the way a run does. The engine reads the source afresh every
	// time, and the watermark deliberately skips a pull request whose
	// reported timestamp has not moved: reusing the list captured before
	// the comment would test a situation that never occurs.
	prs = relist(t, p, srcPath, 1)

	res, err = prsync.Sync(ctx, prs, prsync.Options{
		Mirror: "live", SourceRepo: srcPath, DestRepo: p.QualifiedPath(dstName),
		BranchPrefix: "syncerd/pr", Source: p, Dest: p, SourceConv: p, DestConv: p,
		Comments: true, Reviews: true, Labels: true, State: st,
	})
	if err != nil {
		t.Fatalf("comment sync: %v", err)
	}
	if res.CommentsCreated != 1 {
		t.Fatalf("CommentsCreated = %d, want 1", res.CommentsCreated)
	}

	destComments, err := p.ListComments(ctx, dstPath, found.Number)
	if err != nil {
		t.Fatalf("list destination comments: %v", err)
	}
	var mirrored int
	for _, c := range destComments {
		if strings.Contains(c.Body, "a comment from the live test") {
			mirrored++
		}
	}
	if mirrored != 1 {
		t.Errorf("the destination carries %d copies of the comment, want exactly 1", mirrored)
	}

	// Finally, the source pull request closes and the mirror must follow.
	if err := p.ClosePullRequest(ctx, srcPath, number); err != nil {
		t.Fatalf("close the source pull request: %v", err)
	}

	// A closed pull request drops out of the open listing, which is
	// exactly the case that used to leave the destination open forever.
	res, err = prsync.Sync(ctx, nil, prsync.Options{
		Mirror: "live", SourceRepo: srcPath, DestRepo: p.QualifiedPath(dstName),
		BranchPrefix: "syncerd/pr", Source: p, Dest: p, SourceConv: p, DestConv: p,
		Comments: true, Reviews: true, Labels: true, State: st,
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Closed != 1 {
		t.Fatalf("Closed = %d, want 1", res.Closed)
	}

	final, ok, err := p.FindPullRequest(ctx, dstPath, "syncerd/pr/"+fmt.Sprint(number))
	if err != nil || !ok {
		t.Fatalf("find destination pull request after close: %v, %v", err, ok)
	}
	if final.State == vcs.PROpen {
		t.Error("the destination pull request is still open after its source closed")
	}
}

// TestListReposSeesAPrivateRepository proves the fix that started all of
// this against the real API: a private repository under a personal account
// used to be invisible.
func TestListReposSeesAPrivateRepository(t *testing.T) {
	p, owner := liveProvider(t)

	stamp := time.Now().UTC().Format("20060102-150405")
	name := repoPrefix + "private-" + stamp
	createRepo(t, p, name)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}

	want := owner + "/" + name
	for _, r := range repos {
		if r.Path == want {
			return
		}
	}
	t.Errorf("the private repository %s was not discovered among %d repositories", want, len(repos))
}
