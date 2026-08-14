package metrics

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- a focused, independent exposition format validator ----
//
// This does not pull in a dependency; it checks the structural rules the
// task cares about: every sample line has a preceding HELP and matching
// TYPE for its metric, labels are well formed key="value" pairs, and
// values parse as numbers. It is deliberately strict enough to fail on a
// malformed line, not just a missing one.

var expSampleRe = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+(\S+)$`)
var expLabelRe = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)="([^"]*)"$`)

type expSample struct {
	metric string
	labels map[string]string
	value  float64
}

// validateExposition parses content as Prometheus text exposition format
// and fails the test on any structural violation. It returns the parsed
// samples for further assertions.
func validateExposition(t *testing.T, content string) []expSample {
	t.Helper()

	if content == "" {
		t.Fatal("empty exposition content")
	}
	if !strings.HasSuffix(content, "\n") {
		t.Fatal("exposition content must end with a newline")
	}
	if strings.HasSuffix(content, "\n\n") {
		t.Fatal("exposition content must not end with a blank line")
	}

	help := map[string]bool{}
	typ := map[string]string{}
	seenSeries := map[string]bool{} // metric name -> whether any sample seen yet, to detect interspersed families
	var samples []expSample

	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var lastMetric string
	for i, line := range lines {
		switch {
		case line == "":
			lastMetric = ""
			continue
		case strings.HasPrefix(line, "# HELP "):
			rest := strings.TrimPrefix(line, "# HELP ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) != 2 || parts[0] == "" {
				t.Fatalf("line %d: malformed HELP line: %q", i, line)
			}
			name := parts[0]
			if seenSeries[name] {
				t.Fatalf("line %d: HELP for %s appears after its samples, family is not contiguous", i, name)
			}
			help[name] = true
			lastMetric = name
		case strings.HasPrefix(line, "# TYPE "):
			rest := strings.TrimPrefix(line, "# TYPE ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) != 2 || parts[0] == "" {
				t.Fatalf("line %d: malformed TYPE line: %q", i, line)
			}
			name, kind := parts[0], parts[1]
			if name != lastMetric {
				t.Fatalf("line %d: TYPE for %s does not immediately follow HELP for %s", i, name, lastMetric)
			}
			if !help[name] {
				t.Fatalf("line %d: TYPE for %s with no preceding HELP", i, name)
			}
			typ[name] = kind
		case strings.HasPrefix(line, "#"):
			t.Fatalf("line %d: unexpected comment line: %q", i, line)
		default:
			m := expSampleRe.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("line %d: malformed sample line: %q", i, line)
			}
			name, labelStr, valueStr := m[1], m[2], m[3]
			if !help[name] {
				t.Fatalf("line %d: sample for %s with no HELP", i, name)
			}
			if _, ok := typ[name]; !ok {
				t.Fatalf("line %d: sample for %s with no TYPE", i, name)
			}
			if name != lastMetric {
				t.Fatalf("line %d: sample for %s does not belong to the current family (%s), family is not contiguous", i, name, lastMetric)
			}

			labels := map[string]string{}
			if labelStr != "" {
				for _, part := range strings.Split(labelStr, ",") {
					lm := expLabelRe.FindStringSubmatch(part)
					if lm == nil {
						t.Fatalf("line %d: malformed label %q in line %q", i, part, line)
					}
					labels[lm[1]] = lm[2]
				}
			}

			val, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				t.Fatalf("line %d: non numeric value %q in line %q", i, valueStr, line)
			}
			if math.IsNaN(val) {
				t.Fatalf("line %d: NaN value in line %q", i, line)
			}

			seenSeries[name] = true
			samples = append(samples, expSample{metric: name, labels: labels, value: val})
		}
	}

	return samples
}

func findSample(samples []expSample, metric string, labels map[string]string) (expSample, bool) {
	for _, s := range samples {
		if s.metric != metric {
			continue
		}
		match := true
		for k, v := range labels {
			if s.labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return s, true
		}
	}
	return expSample{}, false
}

func TestWriteTextfile_SuccessfulRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	end := start.Add(12317 * time.Millisecond)

	m := RunMetrics{
		Command:   "git-sync",
		Success:   true,
		StartedAt: start,
		EndedAt:   end,
		Succeeded: 3,
		Skipped:   7,
		Failed:    1,
	}
	if err := WriteTextfile(path, m); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	content := string(b)
	samples := validateExposition(t, content)

	labels := map[string]string{"command": "git-sync"}

	s, ok := findSample(samples, "syncerd_last_run_unixtime", labels)
	if !ok || s.value != float64(end.Unix()) {
		t.Errorf("syncerd_last_run_unixtime = %+v, ok=%v, want %d", s, ok, end.Unix())
	}

	s, ok = findSample(samples, "syncerd_last_success_unixtime", labels)
	if !ok || s.value != float64(end.Unix()) {
		t.Errorf("syncerd_last_success_unixtime = %+v, ok=%v, want %d", s, ok, end.Unix())
	}

	s, ok = findSample(samples, "syncerd_last_run_success", labels)
	if !ok || s.value != 1 {
		t.Errorf("syncerd_last_run_success = %+v, ok=%v, want 1", s, ok)
	}

	s, ok = findSample(samples, "syncerd_last_run_duration_seconds", labels)
	if !ok || math.Abs(s.value-12.317) > 1e-9 {
		t.Errorf("syncerd_last_run_duration_seconds = %+v, ok=%v, want ~12.317", s, ok)
	}

	wantItems := map[string]float64{"succeeded": 3, "skipped": 7, "failed": 1}
	for result, want := range wantItems {
		s, ok = findSample(samples, "syncerd_last_run_items", map[string]string{"command": "git-sync", "result": result})
		if !ok || s.value != want {
			t.Errorf("syncerd_last_run_items{result=%s} = %+v, ok=%v, want %v", result, s, ok, want)
		}
	}
}

func TestWriteTextfile_FailedRunCarriesForwardSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	successEnd := start.Add(5 * time.Second)
	ok1 := RunMetrics{
		Command: "git-sync", Success: true,
		StartedAt: start, EndedAt: successEnd,
		Succeeded: 2,
	}
	if err := WriteTextfile(path, ok1); err != nil {
		t.Fatalf("write success: %v", err)
	}

	failStart := successEnd.Add(time.Hour)
	failEnd := failStart.Add(2 * time.Second)
	fail := RunMetrics{
		Command: "git-sync", Success: false,
		StartedAt: failStart, EndedAt: failEnd,
		Failed: 1,
	}
	if err := WriteTextfile(path, fail); err != nil {
		t.Fatalf("write failure: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	samples := validateExposition(t, string(b))

	labels := map[string]string{"command": "git-sync"}

	s, ok := findSample(samples, "syncerd_last_success_unixtime", labels)
	if !ok {
		t.Fatal("expected syncerd_last_success_unixtime to be carried forward, found none")
	}
	if s.value != float64(successEnd.Unix()) {
		t.Errorf("syncerd_last_success_unixtime = %v, want carried forward %d", s.value, successEnd.Unix())
	}

	s, ok = findSample(samples, "syncerd_last_run_success", labels)
	if !ok || s.value != 0 {
		t.Errorf("syncerd_last_run_success = %+v, ok=%v, want 0", s, ok)
	}

	s, ok = findSample(samples, "syncerd_last_run_unixtime", labels)
	if !ok || s.value != float64(failEnd.Unix()) {
		t.Errorf("syncerd_last_run_unixtime = %+v, ok=%v, want the failed run's end %d", s, ok, failEnd.Unix())
	}
}

func TestWriteTextfile_FailedRunNoPreviousOmitsSuccessSeries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	m := RunMetrics{
		Command: "git-sync", Success: false,
		StartedAt: start, EndedAt: start.Add(time.Second),
		Failed: 1,
	}
	if err := WriteTextfile(path, m); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(b)
	samples := validateExposition(t, content)

	if _, ok := findSample(samples, "syncerd_last_success_unixtime", map[string]string{"command": "git-sync"}); ok {
		t.Error("syncerd_last_success_unixtime must be omitted when there is no previous value, not written as 0")
	}
	if strings.Contains(content, "syncerd_last_success_unixtime") {
		t.Error("syncerd_last_success_unixtime HELP/TYPE must not appear at all when there is no series to emit")
	}

	s, ok := findSample(samples, "syncerd_last_run_success", map[string]string{"command": "git-sync"})
	if !ok || s.value != 0 {
		t.Errorf("syncerd_last_run_success = %+v, ok=%v, want 0", s, ok)
	}
}

func TestWriteTextfile_PreservesOtherCommandSeries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	syncRun := RunMetrics{
		Command: "sync", Success: true,
		StartedAt: start, EndedAt: start.Add(3 * time.Second),
		Succeeded: 5,
	}
	if err := WriteTextfile(path, syncRun); err != nil {
		t.Fatalf("write sync: %v", err)
	}

	gitSyncRun := RunMetrics{
		Command: "git-sync", Success: true,
		StartedAt: start.Add(time.Minute), EndedAt: start.Add(time.Minute + 4*time.Second),
		Succeeded: 2,
	}
	if err := WriteTextfile(path, gitSyncRun); err != nil {
		t.Fatalf("write git-sync: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	samples := validateExposition(t, string(b))

	if _, ok := findSample(samples, "syncerd_last_run_unixtime", map[string]string{"command": "sync"}); !ok {
		t.Error("sync series lost after writing git-sync")
	}
	if _, ok := findSample(samples, "syncerd_last_run_unixtime", map[string]string{"command": "git-sync"}); !ok {
		t.Error("git-sync series missing")
	}
	if s, ok := findSample(samples, "syncerd_last_run_items", map[string]string{"command": "sync", "result": "succeeded"}); !ok || s.value != 5 {
		t.Errorf("sync items overwritten: %+v ok=%v", s, ok)
	}

	// Now write sync again and confirm git-sync survives, the reverse direction.
	syncRun2 := RunMetrics{
		Command: "sync", Success: false,
		StartedAt: start.Add(2 * time.Minute), EndedAt: start.Add(2*time.Minute + time.Second),
		Failed: 9,
	}
	if err := WriteTextfile(path, syncRun2); err != nil {
		t.Fatalf("write sync again: %v", err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	samples = validateExposition(t, string(b))
	if _, ok := findSample(samples, "syncerd_last_run_unixtime", map[string]string{"command": "git-sync"}); !ok {
		t.Error("git-sync series lost after rewriting sync")
	}
	if s, ok := findSample(samples, "syncerd_last_run_items", map[string]string{"command": "git-sync", "result": "succeeded"}); !ok || s.value != 2 {
		t.Errorf("git-sync items disturbed: %+v ok=%v", s, ok)
	}
}

func TestWriteTextfile_CorruptExistingFileDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	if err := os.WriteFile(path, []byte("this is not { valid prometheus\x00text at all\n===\n"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	m := RunMetrics{
		Command: "git-sync", Success: false,
		StartedAt: time.Now(), EndedAt: time.Now(),
		Failed: 1,
	}
	if err := WriteTextfile(path, m); err != nil {
		t.Fatalf("WriteTextfile must tolerate a corrupt existing file: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	samples := validateExposition(t, string(b))
	if _, ok := findSample(samples, "syncerd_last_success_unixtime", map[string]string{"command": "git-sync"}); ok {
		t.Error("no previous value should have been parsed out of the corrupt file")
	}
}

func TestWriteAtomic_CreatesMissingParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "syncerd.prom")

	m := RunMetrics{Command: "sync", Success: true, StartedAt: time.Now(), EndedAt: time.Now()}
	if err := WriteTextfile(path, m); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
}

func TestWriteAtomic_NoTmpLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	m := RunMetrics{Command: "sync", Success: true, StartedAt: time.Now(), EndedAt: time.Now()}
	if err := WriteTextfile(path, m); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temp file left behind: %v", matches)
	}
}

func TestWriteAtomic_NoTmpLeftWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	// path is a directory, so the rename onto it must fail.
	path := filepath.Join(dir, "syncerd.prom")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := RunMetrics{Command: "sync", Success: true, StartedAt: time.Now(), EndedAt: time.Now()}
	if err := WriteTextfile(path, m); err == nil {
		t.Fatal("expected WriteTextfile to fail when path is an existing directory")
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temp file left behind after failed rename: %v", matches)
	}
}

func TestDuration_PrecisionAndNoNegativeOrNaN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncerd.prom")

	now := time.Now()
	// Zero length run: started and ended at the same instant.
	m := RunMetrics{Command: "sync", Success: true, StartedAt: now, EndedAt: now}
	if err := WriteTextfile(path, m); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	samples := validateExposition(t, string(b))
	s, ok := findSample(samples, "syncerd_last_run_duration_seconds", map[string]string{"command": "sync"})
	if !ok {
		t.Fatal("missing syncerd_last_run_duration_seconds")
	}
	if s.value != 0 {
		t.Errorf("zero length run duration = %v, want 0", s.value)
	}
	if s.value < 0 || math.IsNaN(s.value) {
		t.Errorf("duration must not be negative or NaN, got %v", s.value)
	}

	// EndedAt before StartedAt (clock skew, or a caller bug) must still
	// produce a sane, non-negative value rather than propagate garbage
	// into an alerting pipeline.
	m2 := RunMetrics{Command: "sync", Success: true, StartedAt: now, EndedAt: now.Add(-time.Hour)}
	if err := WriteTextfile(path, m2); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	samples = validateExposition(t, string(b))
	s, ok = findSample(samples, "syncerd_last_run_duration_seconds", map[string]string{"command": "sync"})
	if !ok {
		t.Fatal("missing syncerd_last_run_duration_seconds")
	}
	if s.value < 0 {
		t.Errorf("duration must never be negative, got %v", s.value)
	}
}
