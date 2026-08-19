package vcs

import "testing"

func TestFilterMatch(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		r    Repo
		want bool
	}{
		{
			name: "empty include matches everything",
			f:    Filter{},
			r:    Repo{Name: "anything"},
			want: true,
		},
		{
			name: "include glob matches",
			f:    Filter{Include: []string{"terraform-*"}},
			r:    Repo{Name: "terraform-aws-vpc"},
			want: true,
		},
		{
			name: "include glob excludes non-matching",
			f:    Filter{Include: []string{"terraform-*"}},
			r:    Repo{Name: "docs-site"},
			want: false,
		},
		{
			name: "exclude wins over include",
			f:    Filter{Include: []string{"terraform-*"}, Exclude: []string{"*-sandbox"}},
			r:    Repo{Name: "terraform-sandbox"},
			want: false,
		},
		{
			name: "archived skipped when requested",
			f:    Filter{SkipArchived: true},
			r:    Repo{Name: "old", Archived: true},
			want: false,
		},
		{
			name: "archived kept when not requested",
			f:    Filter{SkipArchived: false},
			r:    Repo{Name: "old", Archived: true},
			want: true,
		},
		{
			name: "fork skipped when requested",
			f:    Filter{SkipForks: true},
			r:    Repo{Name: "forked", Fork: true},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Match(tc.r); got != tc.want {
				t.Errorf("Match(%+v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

func TestApplyPreservesOrder(t *testing.T) {
	repos := []Repo{
		{Name: "terraform-a"},
		{Name: "docs"},
		{Name: "terraform-b"},
	}
	got := Apply(Filter{Include: []string{"terraform-*"}}, repos)
	if len(got) != 2 || got[0].Name != "terraform-a" || got[1].Name != "terraform-b" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestFilterBadGlobDoesNotMatch(t *testing.T) {
	// path.Match returns ErrBadPattern for an unterminated character class.
	f := Filter{Include: []string{"[invalid"}}
	if f.Match(Repo{Name: "anything"}) {
		t.Fatal("a malformed include pattern must not match")
	}
}

func TestAMalformedExcludeDropsRatherThanMirrors(t *testing.T) {
	f := Filter{Exclude: []string{"internal-[secret"}}
	if f.Match(Repo{Name: "internal-secrets"}) {
		t.Error("a pattern that cannot be parsed must not mirror the repository it was written to exclude")
	}
}
