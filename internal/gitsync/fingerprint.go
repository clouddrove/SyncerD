// Package gitsync mirrors git repositories between hosting providers.
package gitsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Ref is one entry from git ls-remote.
type Ref struct {
	Name string
	SHA  string
}

// ParseLsRemote reads the tab separated output of git ls-remote. Blank
// lines are ignored; any other malformed line is an error, because
// silently dropping a ref would corrupt the fingerprint and cause a
// missed sync.
func ParseLsRemote(out string) ([]Ref, error) {
	var refs []Ref
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, name, ok := strings.Cut(line, "\t")
		if !ok || sha == "" || name == "" {
			return nil, fmt.Errorf("ls-remote line %d malformed: %q", i+1, line)
		}
		refs = append(refs, Ref{Name: name, SHA: sha})
	}
	return refs, nil
}

// Fingerprint hashes a ref set so an unchanged repository can be skipped
// without cloning. Sorting makes the result independent of server ordering.
func Fingerprint(refs []Ref) string {
	lines := make([]string, 0, len(refs))
	for _, r := range refs {
		lines = append(lines, r.SHA+"\t"+r.Name)
	}
	sort.Strings(lines)

	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
