package codecommit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codecommit"
	"github.com/aws/aws-sdk-go-v2/service/codecommit/types"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// fakeAPI is a test double for the api interface. Each method is backed by
// an injectable function so individual tests only wire up what they need.
type fakeAPI struct {
	mu sync.Mutex

	listFn   func(ctx context.Context, in *codecommit.ListRepositoriesInput) (*codecommit.ListRepositoriesOutput, error)
	getFn    func(ctx context.Context, in *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error)
	createFn func(ctx context.Context, in *codecommit.CreateRepositoryInput) (*codecommit.CreateRepositoryOutput, error)
	updateFn func(ctx context.Context, in *codecommit.UpdateDefaultBranchInput) (*codecommit.UpdateDefaultBranchOutput, error)

	createCalls int

	listPRFn   func(ctx context.Context, in *codecommit.ListPullRequestsInput) (*codecommit.ListPullRequestsOutput, error)
	getPRFn    func(ctx context.Context, in *codecommit.GetPullRequestInput) (*codecommit.GetPullRequestOutput, error)
	createPRFn func(ctx context.Context, in *codecommit.CreatePullRequestInput) (*codecommit.CreatePullRequestOutput, error)
	approvalFn func(ctx context.Context, in *codecommit.GetPullRequestApprovalStatesInput) (*codecommit.GetPullRequestApprovalStatesOutput, error)
	commentsFn func(ctx context.Context, in *codecommit.GetCommentsForPullRequestInput) (*codecommit.GetCommentsForPullRequestOutput, error)
	postFn     func(ctx context.Context, in *codecommit.PostCommentForPullRequestInput) (*codecommit.PostCommentForPullRequestOutput, error)

	listPRCalls    int
	getPRCalls     int
	titleUpdates   []string
	descUpdates    []string
	statusUpdates  []string
	commentUpdates []string
	commentDeletes []string
	posted         []*codecommit.PostCommentForPullRequestInput
}

func (f *fakeAPI) ListRepositories(ctx context.Context, in *codecommit.ListRepositoriesInput, _ ...func(*codecommit.Options)) (*codecommit.ListRepositoriesOutput, error) {
	return f.listFn(ctx, in)
}

func (f *fakeAPI) GetRepository(ctx context.Context, in *codecommit.GetRepositoryInput, _ ...func(*codecommit.Options)) (*codecommit.GetRepositoryOutput, error) {
	return f.getFn(ctx, in)
}

func (f *fakeAPI) CreateRepository(ctx context.Context, in *codecommit.CreateRepositoryInput, _ ...func(*codecommit.Options)) (*codecommit.CreateRepositoryOutput, error) {
	f.mu.Lock()
	f.createCalls++
	f.mu.Unlock()
	return f.createFn(ctx, in)
}

func (f *fakeAPI) UpdateDefaultBranch(ctx context.Context, in *codecommit.UpdateDefaultBranchInput, _ ...func(*codecommit.Options)) (*codecommit.UpdateDefaultBranchOutput, error) {
	return f.updateFn(ctx, in)
}

func strPtr(s string) *string { return &s }

// newProvider returns a Provider whose client factory always returns fake,
// without touching AWS. It also reports how many times the factory ran.
func newProvider(t *testing.T, fake *fakeAPI) *Provider {
	t.Helper()
	p, err := New(Config{Name: "cc", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	p.factory = func(context.Context, string) (api, error) {
		return fake, nil
	}
	return p
}

func TestListReposPaginatesAcrossTwoPages(t *testing.T) {
	fake := &fakeAPI{
		listFn: func(_ context.Context, in *codecommit.ListRepositoriesInput) (*codecommit.ListRepositoriesOutput, error) {
			if in.NextToken == nil {
				return &codecommit.ListRepositoriesOutput{
					Repositories: []types.RepositoryNameIdPair{{RepositoryName: strPtr("first")}},
					NextToken:    strPtr("page-2"),
				}, nil
			}
			if *in.NextToken == "page-2" {
				return &codecommit.ListRepositoriesOutput{
					Repositories: []types.RepositoryNameIdPair{{RepositoryName: strPtr("second")}},
				}, nil
			}
			return nil, fmt.Errorf("unexpected token %q", *in.NextToken)
		},
		getFn: func(_ context.Context, in *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return &codecommit.GetRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{
				RepositoryName: in.RepositoryName,
				DefaultBranch:  strPtr("main"),
			}}, nil
		},
	}
	p := newProvider(t, fake)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}
	if repos[0].Name != "first" || repos[1].Name != "second" {
		t.Errorf("names = %q, %q; want first, second", repos[0].Name, repos[1].Name)
	}
}

func TestListReposRefusesRepeatedToken(t *testing.T) {
	fake := &fakeAPI{
		listFn: func(context.Context, *codecommit.ListRepositoriesInput) (*codecommit.ListRepositoriesOutput, error) {
			return &codecommit.ListRepositoriesOutput{
				Repositories: []types.RepositoryNameIdPair{{RepositoryName: strPtr("r")}},
				NextToken:    strPtr("same-token"),
			}, nil
		},
		getFn: func(context.Context, *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return &codecommit.GetRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{RepositoryName: strPtr("r")}}, nil
		},
	}
	p := newProvider(t, fake)

	_, err := p.ListRepos(context.Background())
	if err == nil {
		t.Fatal("expected an error from a repeated next token, got nil")
	}
}

func TestListReposEnrichesAndPreservesOrder(t *testing.T) {
	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"}
	fake := &fakeAPI{
		listFn: func(context.Context, *codecommit.ListRepositoriesInput) (*codecommit.ListRepositoriesOutput, error) {
			var pairs []types.RepositoryNameIdPair
			for _, n := range names {
				pairs = append(pairs, types.RepositoryNameIdPair{RepositoryName: strPtr(n)})
			}
			return &codecommit.ListRepositoriesOutput{Repositories: pairs}, nil
		},
		getFn: func(_ context.Context, in *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			name := *in.RepositoryName
			return &codecommit.GetRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{
				RepositoryName: in.RepositoryName,
				DefaultBranch:  strPtr("main"),
				CloneUrlHttp:   strPtr("https://git-codecommit.us-east-1.amazonaws.com/v1/repos/" + name),
			}}, nil
		},
	}
	p := newProvider(t, fake)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != len(names) {
		t.Fatalf("got %d repos, want %d", len(repos), len(names))
	}
	for i, n := range names {
		if repos[i].Name != n {
			t.Errorf("repos[%d].Name = %q, want %q (order not preserved)", i, repos[i].Name, n)
		}
		if repos[i].DefaultBranch != "main" {
			t.Errorf("repos[%d].DefaultBranch = %q, want main", i, repos[i].DefaultBranch)
		}
		want := "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/" + n
		if repos[i].CloneURL != want {
			t.Errorf("repos[%d].CloneURL = %q, want %q", i, repos[i].CloneURL, want)
		}
	}
}

func TestListReposNilDefaultBranchYieldsEmptyWithoutPanic(t *testing.T) {
	fake := &fakeAPI{
		listFn: func(context.Context, *codecommit.ListRepositoriesInput) (*codecommit.ListRepositoriesOutput, error) {
			return &codecommit.ListRepositoriesOutput{
				Repositories: []types.RepositoryNameIdPair{{RepositoryName: strPtr("empty-repo")}},
			}, nil
		},
		getFn: func(_ context.Context, in *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return &codecommit.GetRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{
				RepositoryName: in.RepositoryName,
				DefaultBranch:  nil,
			}}, nil
		},
	}
	p := newProvider(t, fake)

	repos, err := p.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(repos))
	}
	if !repos[0].Empty {
		t.Errorf("Empty = false, want true for a nil default branch")
	}
	if repos[0].DefaultBranch != "" {
		t.Errorf("DefaultBranch = %q, want empty", repos[0].DefaultBranch)
	}
}

func TestEnsureRepoCreatesOnNotExist(t *testing.T) {
	fake := &fakeAPI{
		getFn: func(context.Context, *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return nil, &types.RepositoryDoesNotExistException{Message: strPtr("nope")}
		},
		createFn: func(_ context.Context, in *codecommit.CreateRepositoryInput) (*codecommit.CreateRepositoryOutput, error) {
			return &codecommit.CreateRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{
				RepositoryName: in.RepositoryName,
				DefaultBranch:  strPtr("main"),
			}}, nil
		},
	}
	p := newProvider(t, fake)

	repo, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "new-repo"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if repo.Name != "new-repo" {
		t.Errorf("Name = %q, want new-repo", repo.Name)
	}
	if fake.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", fake.createCalls)
	}
}

func TestEnsureRepoIsIdempotentWhenPresent(t *testing.T) {
	fake := &fakeAPI{
		getFn: func(_ context.Context, in *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return &codecommit.GetRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{
				RepositoryName: in.RepositoryName,
				DefaultBranch:  strPtr("main"),
			}}, nil
		},
		createFn: func(context.Context, *codecommit.CreateRepositoryInput) (*codecommit.CreateRepositoryOutput, error) {
			t.Fatal("CreateRepository should not be called when the repository already exists")
			return nil, nil
		},
	}
	p := newProvider(t, fake)

	repo, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "existing-repo"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if repo.Name != "existing-repo" {
		t.Errorf("Name = %q, want existing-repo", repo.Name)
	}
	if fake.createCalls != 0 {
		t.Errorf("createCalls = %d, want 0", fake.createCalls)
	}
}

func TestEnsureRepoDoesNotCreateOnDifferentError(t *testing.T) {
	fake := &fakeAPI{
		getFn: func(context.Context, *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return nil, &types.InvalidRepositoryNameException{Message: strPtr("invalid")}
		},
		createFn: func(context.Context, *codecommit.CreateRepositoryInput) (*codecommit.CreateRepositoryOutput, error) {
			t.Fatal("CreateRepository should not be called on a non not-found error")
			return nil, nil
		},
	}
	p := newProvider(t, fake)

	_, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "some-repo"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var invalidName *types.InvalidRepositoryNameException
	if !errors.As(err, &invalidName) {
		t.Errorf("error does not wrap InvalidRepositoryNameException: %v", err)
	}
	if fake.createCalls != 0 {
		t.Errorf("createCalls = %d, want 0", fake.createCalls)
	}
}

func TestEnsureRepoRejectsNestedName(t *testing.T) {
	fake := &fakeAPI{}
	p := newProvider(t, fake)

	_, err := p.EnsureRepo(context.Background(), vcs.RepoSpec{Path: "team/repo"})
	if err == nil {
		t.Fatal("expected an error for a nested name, got nil")
	}
}

func TestSetDefaultBranchRejectsNestedName(t *testing.T) {
	fake := &fakeAPI{}
	p := newProvider(t, fake)

	err := p.SetDefaultBranch(context.Background(), "team/repo", "main")
	if err == nil {
		t.Fatal("expected an error for a nested name, got nil")
	}
}

func TestGitCredentialReturnsBasicPairWhenConfigured(t *testing.T) {
	p, err := New(Config{Name: "cc", Region: "us-east-1", GitUsername: "git-user", GitPassword: "git-pass"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	cred, err := p.GitCredential(context.Background())
	if err != nil {
		t.Fatalf("GitCredential: %v", err)
	}
	if cred.Kind != vcs.CredBasic {
		t.Errorf("Kind = %v, want CredBasic", cred.Kind)
	}
	if cred.User != "git-user" || cred.Secret != "git-pass" {
		t.Errorf("User/Secret = %q/%q, want git-user/git-pass", cred.User, cred.Secret)
	}
}

func TestGitCredentialErrorsWhenNotConfigured(t *testing.T) {
	p, err := New(Config{Name: "cc", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, err = p.GitCredential(context.Background())
	if err == nil {
		t.Fatal("expected an error when no git credentials are configured, got nil")
	}
}

func TestClientConstructedExactlyOnceUnderConcurrentCalls(t *testing.T) {
	fake := &fakeAPI{
		getFn: func(_ context.Context, in *codecommit.GetRepositoryInput) (*codecommit.GetRepositoryOutput, error) {
			return &codecommit.GetRepositoryOutput{RepositoryMetadata: &types.RepositoryMetadata{RepositoryName: in.RepositoryName}}, nil
		},
	}

	p, err := New(Config{Name: "cc", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var factoryCalls int
	var mu sync.Mutex
	p.factory = func(context.Context, string) (api, error) {
		mu.Lock()
		factoryCalls++
		mu.Unlock()
		return fake, nil
	}

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.clientCached(context.Background()); err != nil {
				t.Errorf("clientCached: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if factoryCalls != 1 {
		t.Errorf("factory called %d times, want 1", factoryCalls)
	}
}

func TestSupportsNestingFalse(t *testing.T) {
	p, err := New(Config{Name: "cc", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p.SupportsNesting() {
		t.Error("SupportsNesting() = true, want false")
	}
}

func TestCloneURL(t *testing.T) {
	p, err := New(Config{Name: "cc", Region: "eu-west-1"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	want := "https://git-codecommit.eu-west-1.amazonaws.com/v1/repos/my-repo"
	if got := p.CloneURL("my-repo"); got != want {
		t.Errorf("CloneURL = %q, want %q", got, want)
	}
}

func TestNewRequiresRegion(t *testing.T) {
	if _, err := New(Config{Name: "cc"}); err == nil {
		t.Fatal("expected an error when region is empty, got nil")
	}
}

// The pull request half of the api interface. Each method is a no-op
// unless a test wires it up, so an existing repository test does not have
// to know these exist.

func (f *fakeAPI) ListPullRequests(ctx context.Context, in *codecommit.ListPullRequestsInput, _ ...func(*codecommit.Options)) (*codecommit.ListPullRequestsOutput, error) {
	f.mu.Lock()
	f.listPRCalls++
	f.mu.Unlock()
	if f.listPRFn == nil {
		return &codecommit.ListPullRequestsOutput{}, nil
	}
	return f.listPRFn(ctx, in)
}

func (f *fakeAPI) GetPullRequest(ctx context.Context, in *codecommit.GetPullRequestInput, _ ...func(*codecommit.Options)) (*codecommit.GetPullRequestOutput, error) {
	f.mu.Lock()
	f.getPRCalls++
	f.mu.Unlock()
	if f.getPRFn == nil {
		return &codecommit.GetPullRequestOutput{}, nil
	}
	return f.getPRFn(ctx, in)
}

func (f *fakeAPI) CreatePullRequest(ctx context.Context, in *codecommit.CreatePullRequestInput, _ ...func(*codecommit.Options)) (*codecommit.CreatePullRequestOutput, error) {
	if f.createPRFn == nil {
		return &codecommit.CreatePullRequestOutput{}, nil
	}
	return f.createPRFn(ctx, in)
}

func (f *fakeAPI) UpdatePullRequestTitle(ctx context.Context, in *codecommit.UpdatePullRequestTitleInput, _ ...func(*codecommit.Options)) (*codecommit.UpdatePullRequestTitleOutput, error) {
	f.mu.Lock()
	f.titleUpdates = append(f.titleUpdates, aws.ToString(in.Title))
	f.mu.Unlock()
	return &codecommit.UpdatePullRequestTitleOutput{}, nil
}

func (f *fakeAPI) UpdatePullRequestDescription(ctx context.Context, in *codecommit.UpdatePullRequestDescriptionInput, _ ...func(*codecommit.Options)) (*codecommit.UpdatePullRequestDescriptionOutput, error) {
	f.mu.Lock()
	f.descUpdates = append(f.descUpdates, aws.ToString(in.Description))
	f.mu.Unlock()
	return &codecommit.UpdatePullRequestDescriptionOutput{}, nil
}

func (f *fakeAPI) UpdatePullRequestStatus(ctx context.Context, in *codecommit.UpdatePullRequestStatusInput, _ ...func(*codecommit.Options)) (*codecommit.UpdatePullRequestStatusOutput, error) {
	f.mu.Lock()
	f.statusUpdates = append(f.statusUpdates, string(in.PullRequestStatus))
	f.mu.Unlock()
	return &codecommit.UpdatePullRequestStatusOutput{}, nil
}

func (f *fakeAPI) GetPullRequestApprovalStates(ctx context.Context, in *codecommit.GetPullRequestApprovalStatesInput, _ ...func(*codecommit.Options)) (*codecommit.GetPullRequestApprovalStatesOutput, error) {
	if f.approvalFn == nil {
		return &codecommit.GetPullRequestApprovalStatesOutput{}, nil
	}
	return f.approvalFn(ctx, in)
}

func (f *fakeAPI) GetCommentsForPullRequest(ctx context.Context, in *codecommit.GetCommentsForPullRequestInput, _ ...func(*codecommit.Options)) (*codecommit.GetCommentsForPullRequestOutput, error) {
	if f.commentsFn == nil {
		return &codecommit.GetCommentsForPullRequestOutput{}, nil
	}
	return f.commentsFn(ctx, in)
}

func (f *fakeAPI) PostCommentForPullRequest(ctx context.Context, in *codecommit.PostCommentForPullRequestInput, _ ...func(*codecommit.Options)) (*codecommit.PostCommentForPullRequestOutput, error) {
	f.mu.Lock()
	f.posted = append(f.posted, in)
	f.mu.Unlock()
	if f.postFn != nil {
		return f.postFn(ctx, in)
	}
	return &codecommit.PostCommentForPullRequestOutput{
		Comment: &types.Comment{CommentId: aws.String("c1")},
	}, nil
}

func (f *fakeAPI) UpdateComment(ctx context.Context, in *codecommit.UpdateCommentInput, _ ...func(*codecommit.Options)) (*codecommit.UpdateCommentOutput, error) {
	f.mu.Lock()
	f.commentUpdates = append(f.commentUpdates, aws.ToString(in.CommentId))
	f.mu.Unlock()
	return &codecommit.UpdateCommentOutput{}, nil
}

func (f *fakeAPI) DeleteCommentContent(ctx context.Context, in *codecommit.DeleteCommentContentInput, _ ...func(*codecommit.Options)) (*codecommit.DeleteCommentContentOutput, error) {
	f.mu.Lock()
	f.commentDeletes = append(f.commentDeletes, aws.ToString(in.CommentId))
	f.mu.Unlock()
	return &codecommit.DeleteCommentContentOutput{}, nil
}
