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
// Exclude always wins.
//
// A malformed pattern always drops the repository, on either list. A typo
// that mirrors nothing is noticed immediately; a typo that mirrors a
// repository somebody wrote an exclude rule to keep out is noticed by
// whoever reads the destination. Configuration validation rejects these
// patterns before a run starts, so this only backstops a caller that did
// not validate.
func (f Filter) Match(r Repo) bool {
	if f.SkipArchived && r.Archived {
		return false
	}
	if f.SkipForks && r.Fork {
		return false
	}
	for _, pat := range f.Exclude {
		ok, err := path.Match(pat, r.Name)
		if err != nil {
			// A malformed exclude pattern must not quietly mirror the
			// repository it was written to keep out. Configuration
			// validation rejects these before a run starts; this is the
			// backstop, and it errs the safe way.
			return false
		}
		if ok {
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
