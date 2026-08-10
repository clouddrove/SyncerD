package gitsync

import "strings"

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
		r.Add(s)
	}
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

func (r *Redactor) rebuild() {
	pairs := make([]string, 0, len(r.secrets)*2)
	for _, s := range r.secrets {
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
