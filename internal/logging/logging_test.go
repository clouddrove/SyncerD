package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

// timestampRe matches the leading "2006/01/02 15:04:05 " that both
// log.Printf(log.LstdFlags) and the text handler render.
var timestampRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// stripTimestamp replaces the leading "2006/01/02 15:04:05 " with a fixed
// token so two lines logged a second apart still compare equal, while
// every other byte, including the timezone the timestamp was rendered in,
// is still compared exactly. A prior version of this test fell back to a
// shape regexp on any divergence and then never compared the two sides to
// each other again, so a handler that rendered in the wrong timezone, or
// dropped a field, or used the wrong separator, still passed as long as it
// matched the same overall shape.
func stripTimestamp(t *testing.T, s string) string {
	t.Helper()
	if !timestampRe.MatchString(s) {
		t.Fatalf("line does not start with the historical timestamp format: %q", s)
	}
	return timestampRe.ReplaceAllString(s, "<TS> ")
}

// TestTextFormatMatchesHistoricalLogPrintf proves the text handler
// reproduces log.Printf(log.LstdFlags) byte for byte, after normalising
// away the one acceptable divergence: the clock's second ticking over
// between the two calls below. Everything else, including the message
// text and the separator, is compared exactly.
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

	if got, want := stripTimestamp(t, newBuf.String()), stripTimestamp(t, oldBuf.String()); got != want {
		t.Errorf("text output drifted from log.Printf once timestamps are normalised:\n old: %q\n new: %q", want, got)
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

	if got, want := stripTimestamp(t, newBuf.String()), stripTimestamp(t, oldBuf.String()); got != want {
		t.Errorf("text output with attributes drifted from log.Printf with none, once timestamps are normalised:\n old: %q\n new: %q", want, got)
	}
}

// TestTextTimestampUsesLocalTimeLikeLogPrintf pins the process to a zone
// with a large, fixed, non-zero offset and checks the text handler renders
// the same wall clock time the standard logger would for the same instant.
//
// The two identity tests above strip the timestamp before comparing, so
// neither can catch a handler that silently switched from Local() to
// UTC(): both sides would still be stripped to the same "<TS> " token.
// This test exists for exactly that mutation. It compares Handle's output
// directly against time.Time.In(loc), for one fixed instant, so it does
// not depend on the host machine's own timezone (which may itself be UTC,
// the exact condition that would let the mutation hide).
//
// Asia/Kolkata is used because its offset (+5:30) has no daylight saving
// transitions to complicate the arithmetic; any zone with a non zero,
// stable offset would do.
func TestTextTimestampUsesLocalTimeLikeLogPrintf(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("tzdata unavailable, skipping: " + err.Error())
	}

	orig := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = orig })

	// A fixed instant, not time.Now(), so the expected string is computed
	// once and cannot drift from the value handed to the handler.
	instant := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	want := instant.In(loc).Format("2006/01/02 15:04:05") + " pinned zone check\n"

	var buf bytes.Buffer
	h := newTextHandler(&buf)
	rec := slog.NewRecord(instant, slog.LevelInfo, "pinned zone check", 0)
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if got := buf.String(); got != want {
		t.Errorf("text handler did not render in local time for a %s instant:\n want: %q\n got:  %q", loc, want, got)
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
