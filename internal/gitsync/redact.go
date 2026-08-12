package gitsync

import (
	"sort"
	"strings"
	"sync"
)

// redactionMarker replaces any configured secret in outbound text.
const redactionMarker = "[REDACTED]"

// minSecretLen guards against a short or accidental secret redacting
// large amounts of unrelated output.
const minSecretLen = 6

// Redactor removes provider secrets from git output before it reaches a
// log line, a GitFailure, or a Slack message.
//
// BuildMirrors constructs one Redactor with every statically configured
// secret and, today, never calls Add again. Azure DevOps in Entra mode and
// AWS CodeCommit obtain short lived credentials per run instead of reading
// a static token from config, so their GitCredential implementations call
// Add from the engine's worker pool while other goroutines may already be
// calling Redact. mu guards secrets and replacer against that concurrent
// access.
//
// Redactor must not be copied after first use; always share it by
// pointer.
type Redactor struct {
	mu       sync.RWMutex
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
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, secret)
	r.rebuild()
}

// rebuild recreates the replacer. Secrets are ordered longest first
// because strings.Replacer resolves overlapping matches by argument
// order, not by longest match. Without this, a secret that is a prefix
// of another would consume the common prefix and leave the longer
// secret's remainder in the output.
//
// rebuild assumes the caller already holds mu for writing; it does not
// lock itself, since NewRedactor calls it before r is shared and Add
// already holds the write lock. Taking the lock here too would deadlock
// against a non reentrant sync.RWMutex.
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
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.replacer == nil {
		return s
	}
	return r.replacer.Replace(s)
}
