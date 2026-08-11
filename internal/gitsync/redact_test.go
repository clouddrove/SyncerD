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
	r := NewRedactor("secret-aaa", "secret-bbb")
	got := r.Redact("secret-aaa then secret-bbb then secret-aaa")
	if got != "[REDACTED] then [REDACTED] then [REDACTED]" {
		t.Errorf("got %q", got)
	}
}

func TestRedactIgnoresEmptyAndShortSecrets(t *testing.T) {
	// An empty or very short secret would redact unrelated text.
	r := NewRedactor("", "ab", "12345")
	in := "a normal message about ab and 12345"
	if got := r.Redact(in); got != in {
		t.Errorf("short or empty secrets must be ignored, got %q", got)
	}

	// Six characters is the threshold and must be redacted.
	atLimit := NewRedactor("123456")
	if got := atLimit.Redact("value 123456 here"); got != "value [REDACTED] here" {
		t.Errorf("a secret at the minimum length must be redacted, got %q", got)
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

func TestRedactPrefersLongestSecret(t *testing.T) {
	// A secret that is a prefix of another must not consume the common
	// prefix and leave the longer secret's remainder behind.
	for _, order := range [][]string{
		{"token1", "token1XYZsecretstuff"},
		{"token1XYZsecretstuff", "token1"},
	} {
		r := NewRedactor(order...)
		got := r.Redact("value=token1XYZsecretstuff end")
		if got != "value=[REDACTED] end" {
			t.Errorf("registration order %v: got %q, want the whole secret redacted", order, got)
		}
	}
}

func TestRedactorAddNilSafe(t *testing.T) {
	var r *Redactor
	r.Add("some-long-secret")
	if got := r.Redact("some-long-secret"); got != "some-long-secret" {
		t.Errorf("nil redactor must pass through, got %q", got)
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
