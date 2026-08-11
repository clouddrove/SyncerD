package gitsync

import (
	"fmt"

	"github.com/clouddrove/syncerd/internal/notify"
)

// maxNotifyLines caps how many repositories a Slack message lists, matching
// the cap image sync uses.
const maxNotifyLines = 50

// BuildMirroredMessage summarises successful mirrors. When detailed is
// true, repositories are grouped by mirror name, the way image sync groups
// by destination.
func BuildMirroredMessage(rep *GitReport, detailed bool) notify.Message {
	n := len(rep.Mirrored)
	title := fmt.Sprintf("%d repository/repositories mirrored", n)
	if rep.Skipped > 0 {
		title = fmt.Sprintf("%s, %d skipped", title, rep.Skipped)
	}

	m := notify.Message{
		Emoji:    ":white_check_mark:",
		Title:    title,
		Color:    notify.ColorSuccess,
		Fallback: fmt.Sprintf("SyncerD: %s", title),
	}

	if n == 0 {
		return m
	}

	line := func(e MirrorEvent) string {
		s := fmt.Sprintf("• `%s` → `%s` (%d pushed", e.SourceRepo, e.DestRepo, e.RefsPushed)
		if e.RefsDeleted > 0 {
			s += fmt.Sprintf(", %d deleted", e.RefsDeleted)
		}
		s += ")"
		if e.Created {
			s += " _created_"
		}
		return s
	}

	if detailed {
		m.Sections = groupedSections(rep.Mirrored, func(e MirrorEvent) string { return e.Mirror }, line)
		return m
	}

	sec := notify.Section{}
	for i, e := range rep.Mirrored {
		if i >= maxNotifyLines {
			sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", n-maxNotifyLines))
			break
		}
		sec.Lines = append(sec.Lines, line(e))
	}
	m.Sections = []notify.Section{sec}
	return m
}

// BuildFailureMessage summarises failures, always naming the stage.
func BuildFailureMessage(rep *GitReport, detailed bool) notify.Message {
	n := len(rep.Failures)
	m := notify.Message{
		Emoji:    ":rotating_light:",
		Title:    fmt.Sprintf("%d git mirror failure(s)", n),
		Color:    notify.ColorFailure,
		Fallback: fmt.Sprintf("SyncerD: %d git mirror failure(s)", n),
	}

	if n == 0 {
		return m
	}

	line := func(f GitFailure) string {
		target := f.SourceRepo
		if f.DestRepo != "" {
			target = fmt.Sprintf("%s → %s", f.SourceRepo, f.DestRepo)
		}
		return fmt.Sprintf("• `%s` [%s]\n  ↳ _%s_", target, f.Stage, f.Error)
	}

	if detailed {
		m.Sections = groupedSections(rep.Failures, func(f GitFailure) string { return f.Mirror }, line)
		return m
	}

	sec := notify.Section{}
	for i, f := range rep.Failures {
		if i >= maxNotifyLines {
			sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", n-maxNotifyLines))
			break
		}
		sec.Lines = append(sec.Lines, line(f))
	}
	m.Sections = []notify.Section{sec}
	return m
}

// groupedSections buckets items by key, preserving first appearance order,
// and caps the total number of lines emitted. A group that is cut short by
// the cap says so in its heading, and the overflow marker sits in its own
// section so it is not read as belonging to the last group.
func groupedSections[T any](items []T, key func(T) string, render func(T) string) []notify.Section {
	byKey := map[string][]T{}
	var order []string
	for _, it := range items {
		k := key(it)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], it)
	}

	sections := make([]notify.Section, 0, len(order)+1)
	remaining := maxNotifyLines

	for _, k := range order {
		list := byKey[k]
		if remaining <= 0 {
			break
		}

		emit := len(list)
		if emit > remaining {
			emit = remaining
		}

		heading := fmt.Sprintf("%s (%d)", k, len(list))
		if emit < len(list) {
			heading = fmt.Sprintf("%s (%d of %d)", k, emit, len(list))
		}

		sec := notify.Section{Heading: heading}
		for _, it := range list[:emit] {
			sec.Lines = append(sec.Lines, render(it))
		}
		sections = append(sections, sec)
		remaining -= emit
	}

	if len(items) > maxNotifyLines {
		sections = append(sections, notify.Section{
			Lines: []string{fmt.Sprintf("_…and %d more_", len(items)-maxNotifyLines)},
		})
	}

	return sections
}
