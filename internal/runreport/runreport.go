// Package runreport writes a machine readable summary of a single run.
//
// Both sync and git-sync build their own report of what happened during a
// run. This package defines one on disk schema that either command's
// report can be converted into, so a consumer parses one shape instead of
// branching on which command produced it.
package runreport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the current shape of Report. Bump it when a field
// changes meaning, so a consumer can refuse a shape it does not
// understand rather than silently misreading it.
const SchemaVersion = 1

// Report is the on disk schema written after a run.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Command       string    `json:"command"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	DurationSecs  float64   `json:"duration_seconds"`
	Success       bool      `json:"success"`
	DryRun        bool      `json:"dry_run"`
	Counts        Counts    `json:"counts"`
	Items         []Item    `json:"items"`
	Failures      []Failure `json:"failures"`
}

// Counts summarises Items and Failures for a consumer that only wants
// totals.
type Counts struct {
	Succeeded int `json:"succeeded"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// Item is one artifact that was acted on. Source and Destination are the
// provider side identifiers; Detail carries the per kind extras, such as
// refs pushed for a repository.
type Item struct {
	Group       string         `json:"group"` // mirror name, or destination name for image sync
	Source      string         `json:"source"`
	Destination string         `json:"destination"`
	Created     bool           `json:"created,omitempty"` // git-sync only: true when the destination repository did not exist and was created
	Detail      map[string]int `json:"detail,omitempty"`  // git-sync only: refs_pushed, refs_deleted; always empty for image sync
}

// Failure is one artifact that failed, and where.
type Failure struct {
	Group       string `json:"group"`
	Source      string `json:"source"`
	Destination string `json:"destination,omitempty"`
	Stage       string `json:"stage,omitempty"`
	Error       string `json:"error"`
}

// NewRunID returns a short, sortable, effectively unique identifier for
// one run: a UTC timestamp followed by a few random hex bytes, for
// example "20260814T091500Z-3f9a2c". The timestamp keeps run IDs sortable
// by time; the random suffix keeps two runs started within the same
// second distinct.
func NewRunID() string {
	var b [3]byte
	// A read failure here is not worth surfacing; a zeroed suffix still
	// yields a valid, if less unique, run ID.
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))
}

// Write marshals r as indented JSON and replaces path in one rename, so a
// crash mid-write cannot leave a truncated report. The parent directory is
// created if missing, and the file is written with mode 0o600.
//
// This duplicates internal/state's writeAtomic on purpose: that helper is
// unexported and lives in a package neither sync package should depend on
// just for a file write, and runreport must stay importable by both
// without pulling internal/state along with it.
func Write(path string, r Report) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create report dir: %w", err)
		}
	}

	tmp := path + ".tmp"
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write report tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Leaving the temp file behind would accumulate one stray file per
		// run for as long as the path stays misconfigured.
		_ = os.Remove(tmp)
		return fmt.Errorf("replace report file: %w", err)
	}
	return nil
}

// WriteRun finalizes rr and writes it to path, the shared logic behind
// both commands' --report flag.
//
// A run can abort before any artifact is attempted, for example a git
// preflight failure or a work directory lock failure, leaving Failures
// empty even though the run did not succeed. runErr is the ground truth
// for the run's outcome, so it overrides whatever the per artifact
// failures implied about Success.
//
// WriteRun is a no-op returning nil when path is empty, since --report is
// optional. Any error it returns is a write failure; the caller decides
// how to surface that, but per the flag's contract it must never be
// treated as a run failure.
func WriteRun(path string, rr Report, runErr error) error {
	if path == "" {
		return nil
	}
	rr.Success = rr.Success && runErr == nil
	return Write(path, rr)
}
