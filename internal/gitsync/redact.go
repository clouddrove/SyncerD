package gitsync

import (
	"sort"
	"strings"
)

// redactionMarker replaces any configured secret in outbound text.
const redactionMarker = "[REDACTED]"

// minSecretLen guards against a short or accidental secret redacting
// large amounts of unrelated output.
const minSecretLen = 6

// Redactor removes provider secrets from git output before it reaches a
// log line, a GitFailure, or a Slack message.
type Redactor struct {
	replacer *strings.Replacer
	secrets  []string
}

// NewRedactor builds a redactor for the given secrets. Empty and very
// short values are ignored.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	for _, s := range secrets {
		if len(s) < minSecretLen {
			continue
		}
		r.secrets = append(r.secrets, s)
	}
	r.rebuild()
	return r
}

// Add registers another secret.
func (r *Redactor) Add(secret string) {
	if r == nil || len(secret) < minSecretLen {
		return
	}
	r.secrets = append(r.secrets, secret)
	r.rebuild()
}

// rebuild recreates the replacer. Secrets are ordered longest first
// because strings.Replacer resolves overlapping matches by argument
// order, not by longest match. Without this, a secret that is a prefix
// of another would consume the common prefix and leave the longer
// secret's remainder in the output.
func (r *Redactor) rebuild() {
	ordered := make([]string, len(r.secrets))
	copy(ordered, r.secrets)
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})

	pairs := make([]string, 0, len(ordered)*2)
	for _, s := range ordered {
		pairs = append(pairs, s, redactionMarker)
	}
	r.replacer = strings.NewReplacer(pairs...)
}

// Redact returns s with every registered secret replaced.
func (r *Redactor) Redact(s string) string {
	if r == nil || r.replacer == nil {
		return s
	}
	return r.replacer.Replace(s)
}
