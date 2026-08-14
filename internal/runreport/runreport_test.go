package runreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func sampleReport() Report {
	started := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	return Report{
		SchemaVersion: SchemaVersion,
		RunID:         "20260814T091500Z-3f9a2c",
		Command:       "git-sync",
		StartedAt:     started,
		EndedAt:       started.Add(5 * time.Second),
		DurationSecs:  5,
		Success:       true,
		DryRun:        false,
		Counts:        Counts{Succeeded: 1, Skipped: 0, Failed: 0},
		Items: []Item{
			{Group: "gh-to-gl", Source: "acme/app", Destination: "acme/app", Detail: map[string]int{"refs_pushed": 3}},
		},
	}
}

func TestWriteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	want := sampleReport()

	if err := Write(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if got.RunID != want.RunID || got.Command != want.Command || got.Success != want.Success {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if len(got.Items) != 1 || got.Items[0].Detail["refs_pushed"] != 3 {
		t.Errorf("Items round-tripped wrong: %+v", got.Items)
	}
}

func TestWriteCreatesMissingParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "report.json")
	if err := Write(path, sampleReport()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected report file to exist: %v", err)
	}
}

func TestWriteLeavesNoTmpFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := Write(path, sampleReport()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no .tmp file left behind, stat err = %v", err)
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	first := sampleReport()
	if err := Write(path, first); err != nil {
		t.Fatalf("write first: %v", err)
	}

	second := sampleReport()
	second.RunID = "20260814T091600Z-000000"
	second.Success = false
	if err := Write(path, second); err != nil {
		t.Fatalf("write second: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunID != second.RunID || got.Success != false {
		t.Errorf("expected overwrite to stick, got %+v", got)
	}
}

func TestWriteFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := Write(path, sampleReport()); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestNewRunIDUniqueAcrossRapidCalls checks that back to back calls, which
// can land within the same wall clock second, never collide because of
// the random suffix.
func TestNewRunIDUniqueAcrossRapidCalls(t *testing.T) {
	const n = 200
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := NewRunID()
		if seen[id] {
			t.Fatalf("duplicate run ID on call %d: %q", i, id)
		}
		seen[id] = true
	}
}

// TestNewRunIDSortsChronologically checks that a run ID generated later
// sorts after one generated earlier. The random suffix only breaks ties
// within the same second, so the assertion is made across a real time gap.
func TestNewRunIDSortsChronologically(t *testing.T) {
	first := NewRunID()
	time.Sleep(1100 * time.Millisecond)
	second := NewRunID()

	got := []string{second, first}
	sort.Strings(got)
	if got[0] != first || got[1] != second {
		t.Fatalf("expected %q to sort before %q, got order %v", first, second, got)
	}
}

// TestNewRunIDFormat checks the ID looks like the documented shape:
// a UTC timestamp, a hyphen, then six hex digits.
func TestNewRunIDFormat(t *testing.T) {
	id := NewRunID()
	if len(id) != len("20060102T150405Z")+1+6 {
		t.Fatalf("unexpected run ID length: %q", id)
	}
	if id[15] != 'Z' || id[16] != '-' {
		t.Fatalf("unexpected run ID shape: %q", id)
	}
	if _, err := time.Parse("20060102T150405Z", id[:16]); err != nil {
		t.Fatalf("timestamp prefix did not parse: %v", err)
	}
}
