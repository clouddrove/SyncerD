package gitsync

import "testing"

func TestRedactReplacesSecrets(t *testing.T) {
	r := NewRedactor("ghp_supersecrettoken", "glpat-anothersecret")

	in := "fatal: could not read from https://x-access-token:ghp_supersecrettoken@github.com/a/b.git"
	got := r.Redact(in)

	if got == in {
		t.Fatal("expected redaction to change the string")
	}
	if contains(got, "ghp_supersecrettoken") {
		t.Errorf("secret leaked: %q", got)
	}
	if !contains(got, "[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", got)
	}
}

func TestRedactHandlesMultipleSecretsAndOccurrences(t *testing.T) {
	r := NewRedactor("aaa", "bbb")
	got := r.Redact("aaa then bbb then aaa")
	if got != "[REDACTED] then [REDACTED] then [REDACTED]" {
		t.Errorf("got %q", got)
	}
}

func TestRedactIgnoresEmptyAndShortSecrets(t *testing.T) {
	// An empty or very short secret would redact unrelated text.
	r := NewRedactor("", "ab")
	in := "a normal message about ab"
	if got := r.Redact(in); got != in {
		t.Errorf("short or empty secrets must be ignored, got %q", got)
	}
}

func TestRedactNilSafe(t *testing.T) {
	var r *Redactor
	if got := r.Redact("hello"); got != "hello" {
		t.Errorf("nil redactor must pass through, got %q", got)
	}
}

func TestRedactorAdd(t *testing.T) {
	r := NewRedactor()
	r.Add("latertoken123")
	if got := r.Redact("uses latertoken123"); contains(got, "latertoken123") {
		t.Errorf("Add must register a secret, got %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
