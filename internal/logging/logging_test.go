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

func TestTextFormatAttributesAppendedAndOmitted(t *testing.T) {
	var buf bytes.Buffer
	if err := Setup("text", &buf); err != nil {
		t.Fatal(err)
	}

	buf.Reset()
	Info("no attrs here")
	if strings.Contains(buf.String(), "=") {
		t.Errorf("expected no key=value pairs when no attrs are passed, got %q", buf.String())
	}

	buf.Reset()
	Info("mirror acme: pushed", "mirror", "acme", "refs_pushed", 3)
	got := buf.String()
	if !strings.Contains(got, " mirror=acme") {
		t.Errorf("expected mirror=acme attribute, got %q", got)
	}
	if !strings.Contains(got, " refs_pushed=3") {
		t.Errorf("expected refs_pushed=3 attribute, got %q", got)
	}
}

func TestTextFormatQuotesValueWithSpace(t *testing.T) {
	var buf bytes.Buffer
	if err := Setup("text", &buf); err != nil {
		t.Fatal(err)
	}

	Info("state changed", "reason", "destination changed from a to b")
	got := buf.String()
	if !strings.Contains(got, `reason="destination changed from a to b"`) {
		t.Errorf("expected quoted value for attribute containing a space, got %q", got)
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
