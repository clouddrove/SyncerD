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
// and caps the total number of lines emitted.
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

	var sections []notify.Section
	seen := 0
	for _, k := range order {
		list := byKey[k]
		sec := notify.Section{Heading: fmt.Sprintf("%s (%d)", k, len(list))}
		for _, it := range list {
			if seen >= maxNotifyLines {
				sec.Lines = append(sec.Lines, fmt.Sprintf("_…and %d more_", len(items)-maxNotifyLines))
				sections = append(sections, sec)
				return sections
			}
			sec.Lines = append(sec.Lines, render(it))
			seen++
		}
		sections = append(sections, sec)
	}
	return sections
}
