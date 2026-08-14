package logging

import (
	"bytes"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"testing"
)

// TestTextFormatMatchesHistoricalLogPrintf proves the text handler
// reproduces log.Printf(log.LstdFlags) byte for byte. Both sides are also
// checked against the same regexp shape, so the test stays robust if the
// clock's second ticks over between the two calls.
func TestTextFormatMatchesHistoricalLogPrintf(t *testing.T) {
	// Capture what the standard logger produces today.
	var oldBuf bytes.Buffer
	std := log.New(&oldBuf, "", log.LstdFlags)
	std.Printf("mirror %s: %s unchanged, skipping", "gh-to-gl", "acme/app")

	// Capture what the new text handler produces.
	var newBuf bytes.Buffer
	if err := Setup("text", &newBuf); err != nil {
		t.Fatal(err)
	}
	Info("mirror gh-to-gl: acme/app unchanged, skipping")

	if oldBuf.String() == newBuf.String() {
		return
	}

	// Only acceptable divergence is the timestamp's second ticking over
	// between the two calls above. Fall back to a shape comparison rather
	// than weakening this to a substring check.
	want := regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} mirror gh-to-gl: acme/app unchanged, skipping\n$`)
	if !want.MatchString(oldBuf.String()) {
		t.Fatalf("standard logger output does not match expected shape: %q", oldBuf.String())
	}
	if !want.MatchString(newBuf.String()) {
		t.Errorf("text output drifted from log.Printf:\n old: %q\n new: %q", oldBuf.String(), newBuf.String())
	}
}

// TestTextFormatIgnoresAttributes proves the case that escaped the earlier
// byte identity test: a call that passes attributes must still produce
// exactly what log.Printf would produce for the message alone, with no
// " key=value" pairs appended. A prior version of this handler appended
// attributes in text mode, which duplicated values the message already
// stated (for example "... (3 pushed, 1 deleted)" gaining a trailing
// "refs_pushed=3 refs_deleted=1").
func TestTextFormatIgnoresAttributes(t *testing.T) {
	var oldBuf bytes.Buffer
	std := log.New(&oldBuf, "", log.LstdFlags)
	std.Printf("mirror %s: %s -> %s (%d pushed, %d deleted)", "gh-to-gl", "acme/app", "acme/app", 3, 1)

	var newBuf bytes.Buffer
	if err := Setup("text", &newBuf); err != nil {
		t.Fatal(err)
	}
	Info("mirror gh-to-gl: acme/app -> acme/app (3 pushed, 1 deleted)",
		"mirror", "gh-to-gl", "source", "acme/app", "destination", "acme/app",
		"refs_pushed", 3, "refs_deleted", 1)

	if oldBuf.String() == newBuf.String() {
		return
	}

	want := regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} mirror gh-to-gl: acme/app -> acme/app \(3 pushed, 1 deleted\)\n$`)
	if !want.MatchString(oldBuf.String()) {
		t.Fatalf("standard logger output does not match expected shape: %q", oldBuf.String())
	}
	if !want.MatchString(newBuf.String()) {
		t.Errorf("text output with attributes drifted from log.Printf with none:\n old: %q\n new: %q", oldBuf.String(), newBuf.String())
	}
}

// TestTextFormatNeverAppendsKeyValuePairs is a direct check that no
// " key=value" text ever appears in text mode output, regardless of what
// attributes a call passes.
func TestTextFormatNeverAppendsKeyValuePairs(t *testing.T) {
	var buf bytes.Buffer
	if err := Setup("text", &buf); err != nil {
		t.Fatal(err)
	}

	Info("mirror acme: pushed", "mirror", "acme", "refs_pushed", 3, "reason", "two words")
	if strings.Contains(buf.String(), "=") {
		t.Errorf("expected no key=value pairs in text mode, got %q", buf.String())
	}
}

func TestJSONFormatEmitsOneObjectPerLineWithFields(t *testing.T) {
	var buf bytes.Buffer
	if err := Setup("json", &buf); err != nil {
		t.Fatal(err)
	}

	Info("mirror acme: pushed", "mirror", "acme", "refs_pushed", 3)
	Warn("something looked odd")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON lines, got %d: %q", len(lines), buf.String())
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not valid JSON: %v", err)
	}
	for _, field := range []string{"time", "level", "msg"} {
		if _, ok := first[field]; !ok {
			t.Errorf("expected field %q in JSON line, got %v", field, first)
		}
	}
	if first["msg"] != "mirror acme: pushed" {
		t.Errorf("unexpected msg field: %v", first["msg"])
	}
	if first["mirror"] != "acme" {
		t.Errorf("expected top level field mirror=acme, got %v", first["mirror"])
	}
	if first["refs_pushed"] != float64(3) {
		t.Errorf("expected top level field refs_pushed=3, got %v", first["refs_pushed"])
	}
	if first["level"] != "INFO" {
		t.Errorf("expected level=INFO, got %v", first["level"])
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line is not valid JSON: %v", err)
	}
	if second["level"] != "WARN" {
		t.Errorf("expected level=WARN, got %v", second["level"])
	}
}

func TestSetupRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	err := Setup("yaml", &buf)
	if err == nil {
		t.Fatal("expected an error for an unknown log format, got nil")
	}
	if !strings.Contains(err.Error(), "text") || !strings.Contains(err.Error(), "json") {
		t.Errorf("expected error to name the valid values (text, json), got %q", err.Error())
	}
}
