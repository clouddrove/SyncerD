// Package metrics writes a Prometheus textfile collector file describing the
// outcome of the last SyncerD run.
//
// SyncerD runs on a cron schedule with no long-lived process, so there is
// nothing to scrape. Prometheus's own guidance for a single node batch job
// is the node_exporter textfile collector rather than the Pushgateway, so
// this package writes a plain ".prom" file instead of opening a listener or
// pushing anywhere.
//
// The one metric that matters most is syncerd_last_success_unixtime: it is
// what turns "the cron silently stopped firing" into an alertable condition
// via time() - syncerd_last_success_unixtime > threshold. Logs show nothing
// when nothing runs, and the Slack integration correctly stays quiet when
// there is nothing to report, so without this metric a dead cron and a
// healthy one look identical from the outside.
package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RunMetrics is the outcome of one run, in the shape the textfile collector
// exposes.
type RunMetrics struct {
	Command   string // "sync" or "git-sync"
	Success   bool
	StartedAt time.Time
	EndedAt   time.Time
	Succeeded int
	Skipped   int
	Failed    int
}

// metricOrder is the fixed order metric families are written in. It is used
// both to build output and to know which families to look for in an
// existing file.
var metricOrder = []string{
	"syncerd_last_run_unixtime",
	"syncerd_last_success_unixtime",
	"syncerd_last_run_success",
	"syncerd_last_run_duration_seconds",
	"syncerd_last_run_items",
}

var metricHelp = map[string]string{
	"syncerd_last_run_unixtime":         "Unix timestamp when the last run finished.",
	"syncerd_last_success_unixtime":     "Unix timestamp of the last run that succeeded.",
	"syncerd_last_run_success":          "Whether the last run succeeded, 1 or 0.",
	"syncerd_last_run_duration_seconds": "How long the last run took.",
	"syncerd_last_run_items":            "How many artifacts the last run handled, by result.",
}

// seriesLine is one already-parsed sample line from an existing textfile.
type seriesLine struct {
	labels map[string]string
	raw    string // the exact line as read, reused verbatim when preserved
	value  string // the numeric value text, extracted from raw
}

// seriesRe matches one Prometheus exposition sample line:
// metric_name{label="value",...} number
var seriesRe = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+(-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)\s*$`)

var labelRe = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)="([^"]*)"$`)

// parseExisting reads and parses path into series lines grouped by metric
// name. Any read or parse failure, including a missing, empty, truncated,
// or otherwise corrupt file, simply yields no series: a metrics problem
// must never fail a run, it only means there is nothing to carry forward.
func parseExisting(path string) map[string][]seriesLine {
	result := map[string][]seriesLine{}

	b, err := os.ReadFile(path)
	if err != nil {
		return result
	}

	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		m := seriesRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, labelStr, value := m[1], m[2], m[3]

		labels, ok := parseLabels(labelStr)
		if !ok {
			continue
		}

		result[name] = append(result[name], seriesLine{labels: labels, raw: line, value: value})
	}

	return result
}

// parseLabels parses a comma separated `key="value"` label list. It returns
// ok=false for anything that does not cleanly match, so a malformed label
// set causes that one line to be dropped rather than misread.
func parseLabels(s string) (map[string]string, bool) {
	labels := map[string]string{}
	if s == "" {
		return labels, true
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		m := labelRe.FindStringSubmatch(part)
		if m == nil {
			return nil, false
		}
		labels[m[1]] = m[2]
	}
	return labels, true
}

// previousSuccessValue returns the raw value text of an existing
// syncerd_last_success_unixtime series for command, if one was found while
// parsing the previous file.
func previousSuccessValue(existing map[string][]seriesLine, command string) (string, bool) {
	for _, sl := range existing["syncerd_last_success_unixtime"] {
		if sl.labels["command"] == command {
			return sl.value, true
		}
	}
	return "", false
}

// otherCommandLines returns the raw lines of an existing metric family that
// belong to a command other than the one currently being written, so they
// pass through untouched.
func otherCommandLines(existing map[string][]seriesLine, metric, command string) []string {
	var lines []string
	for _, sl := range existing[metric] {
		if sl.labels["command"] != command {
			lines = append(lines, sl.raw)
		}
	}
	return lines
}

// render builds the full exposition text for m, preserving any other
// command's series found in existing and carrying forward a previous
// success timestamp when the run failed.
func render(m RunMetrics, existing map[string][]seriesLine) string {
	lines := map[string][]string{}

	for _, name := range metricOrder {
		lines[name] = otherCommandLines(existing, name, m.Command)
	}

	lines["syncerd_last_run_unixtime"] = append(lines["syncerd_last_run_unixtime"],
		fmt.Sprintf(`syncerd_last_run_unixtime{command=%q} %d`, m.Command, m.EndedAt.Unix()))

	if m.Success {
		lines["syncerd_last_success_unixtime"] = append(lines["syncerd_last_success_unixtime"],
			fmt.Sprintf(`syncerd_last_success_unixtime{command=%q} %d`, m.Command, m.EndedAt.Unix()))
	} else if prev, ok := previousSuccessValue(existing, m.Command); ok {
		// Carry the previous success forward unchanged. Dropping it on a
		// failed run would silently break the alert this package exists
		// for, at the exact moment it is needed.
		lines["syncerd_last_success_unixtime"] = append(lines["syncerd_last_success_unixtime"],
			fmt.Sprintf(`syncerd_last_success_unixtime{command=%q} %s`, m.Command, prev))
	}
	// If the run failed and there is no previous value, the series for
	// this command is omitted entirely rather than written as 0, since a
	// zero timestamp would read as "last succeeded in 1970" and could
	// itself fire the staleness alert misleadingly.

	successVal := 0
	if m.Success {
		successVal = 1
	}
	lines["syncerd_last_run_success"] = append(lines["syncerd_last_run_success"],
		fmt.Sprintf(`syncerd_last_run_success{command=%q} %d`, m.Command, successVal))

	duration := m.EndedAt.Sub(m.StartedAt).Seconds()
	if duration < 0 {
		duration = 0
	}
	lines["syncerd_last_run_duration_seconds"] = append(lines["syncerd_last_run_duration_seconds"],
		fmt.Sprintf(`syncerd_last_run_duration_seconds{command=%q} %.3f`, m.Command, duration))

	lines["syncerd_last_run_items"] = append(lines["syncerd_last_run_items"],
		fmt.Sprintf(`syncerd_last_run_items{command=%q,result="succeeded"} %d`, m.Command, m.Succeeded),
		fmt.Sprintf(`syncerd_last_run_items{command=%q,result="skipped"} %d`, m.Command, m.Skipped),
		fmt.Sprintf(`syncerd_last_run_items{command=%q,result="failed"} %d`, m.Command, m.Failed),
	)

	var blocks []string
	for _, name := range metricOrder {
		series := lines[name]
		if len(series) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# HELP %s %s\n", name, metricHelp[name])
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		for _, s := range series {
			b.WriteString(s)
			b.WriteString("\n")
		}
		blocks = append(blocks, strings.TrimSuffix(b.String(), "\n"))
	}

	return strings.Join(blocks, "\n\n") + "\n"
}

// WriteTextfile writes m to path in the Prometheus text exposition format,
// atomically. Any existing series for a command other than m.Command is
// preserved untouched, and a failed run carries the previous
// syncerd_last_success_unixtime forward instead of dropping it.
//
// Two SyncerD processes must not point --metrics-file at the same path.
// The read, modify, write in this function has no lock, so two processes
// racing for one path can lose one of their updates, including
// syncerd_last_success_unixtime, silently: the staleness alert would then
// evaluate over an empty vector and not fire, the exact failure the metric
// exists to prevent. The intended deployment is one file per subcommand
// (one for sync, one for git-sync) in a directory node_exporter's textfile
// collector scrapes, since it merges every ".prom" file in that directory
// on its own.
func WriteTextfile(path string, m RunMetrics) error {
	existing := parseExisting(path)
	content := render(m, existing)
	return writeAtomic(path, content)
}

// writeAtomic writes content to path via a uniquely named temp file plus
// rename.
//
// The temp file name includes a random suffix (os.CreateTemp) rather than
// a fixed path+".tmp", so two writers racing for the same path, which this
// package does not otherwise guard against (see the WriteTextfile doc
// comment), can never both hold the same temp file open and interleave
// their writes into it. Each writer's temp file is always either whole or
// absent; only which writer's rename lands last is still a race.
//
// internal/state has its own writeAtomic, but it JSON-encodes a value; this
// package writes a plain text format, so it is not a fit to share and gets
// its own copy rather than a forced abstraction over two different jobs.
func writeAtomic(path string, content string) error {
	// filepath.Dir never returns an empty string; "." (no directory
	// component) and "/" always already exist.
	dir := filepath.Dir(path)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create metrics dir: %w", err)
		}
	}

	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create metrics tmp: %w", err)
	}
	tmp := f.Name()

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write metrics tmp: %w", err)
	}
	// os.CreateTemp creates the file 0o600; match the world readable mode
	// the metrics file has always had, since node_exporter typically runs
	// as a different user than SyncerD.
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("chmod metrics tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close metrics tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace metrics file: %w", err)
	}
	return nil
}
