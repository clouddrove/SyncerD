package vcs

import "path"

// Filter selects which discovered repositories to mirror.
type Filter struct {
	Include      []string
	Exclude      []string
	SkipArchived bool
	SkipForks    bool
}

// Match reports whether r survives the filter. Patterns are globs matched
// against the repository name. An empty Include list matches everything.
// Exclude always wins. A malformed pattern never matches, so a typo drops
// repositories rather than silently mirroring all of them.
func (f Filter) Match(r Repo) bool {
	if f.SkipArchived && r.Archived {
		return false
	}
	if f.SkipForks && r.Fork {
		return false
	}
	for _, pat := range f.Exclude {
		if ok, err := path.Match(pat, r.Name); err == nil && ok {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, pat := range f.Include {
		if ok, err := path.Match(pat, r.Name); err == nil && ok {
			return true
		}
	}
	return false
}

// Apply returns the repositories matching f, preserving input order.
func Apply(f Filter, repos []Repo) []Repo {
	out := make([]Repo, 0, len(repos))
	for _, r := range repos {
		if f.Match(r) {
			out = append(out, r)
		}
	}
	return out
}
