package gitsync

import (
	"fmt"
	"strings"
	"testing"
)

func sampleReport() *GitReport {
	return &GitReport{
		Mirrored: []MirrorEvent{
			{Mirror: "gh-to-gl", SourceRepo: "acme/app", DestRepo: "mirrors/app", RefsPushed: 3, Created: true},
			{Mirror: "gh-to-gl", SourceRepo: "acme/lib", DestRepo: "mirrors/lib", RefsPushed: 1, RefsDeleted: 2},
			{Mirror: "gl-to-gh", SourceRepo: "mirrors/x", DestRepo: "acme/x", RefsPushed: 1},
		},
		Skipped: 7,
		Failures: []GitFailure{
			{Mirror: "gh-to-gl", SourceRepo: "acme/bad", DestRepo: "mirrors/bad", Stage: "push", Error: "permission denied"},
		},
	}
}

func TestBuildMirroredMessageCompact(t *testing.T) {
	m := BuildMirroredMessage(sampleReport(), false)

	if !strings.Contains(m.Title, "3") {
		t.Errorf("Title should carry the count, got %q", m.Title)
	}
	if !strings.Contains(m.Title, "7 skipped") {
		t.Errorf("Title should report skipped repos, got %q", m.Title)
	}
	if m.Color != successColor() {
		t.Errorf("expected the success color")
	}
	if len(m.Sections) != 1 {
		t.Fatalf("compact format should use one section, got %d", len(m.Sections))
	}
	body := strings.Join(m.Sections[0].Lines, "\n")
	if !strings.Contains(body, "acme/app") || !strings.Contains(body, "mirrors/app") {
		t.Errorf("body missing repo names: %q", body)
	}
}

func TestBuildMirroredMessageDetailedGroupsByMirror(t *testing.T) {
	m := BuildMirroredMessage(sampleReport(), true)

	if len(m.Sections) != 2 {
		t.Fatalf("detailed format should group by mirror, got %d sections", len(m.Sections))
	}
	if !strings.Contains(m.Sections[0].Heading, "gh-to-gl") {
		t.Errorf("first heading = %q", m.Sections[0].Heading)
	}
	if !strings.Contains(m.Sections[0].Heading, "2") {
		t.Errorf("heading should carry the per mirror count, got %q", m.Sections[0].Heading)
	}
	if !strings.Contains(m.Sections[1].Heading, "gl-to-gh") {
		t.Errorf("second heading = %q", m.Sections[1].Heading)
	}
}

func TestBuildFailureMessageIncludesStage(t *testing.T) {
	m := BuildFailureMessage(sampleReport(), false)

	if !strings.Contains(m.Title, "1") {
		t.Errorf("Title = %q", m.Title)
	}
	body := strings.Join(m.Sections[0].Lines, "\n")
	if !strings.Contains(body, "push") {
		t.Errorf("failure body must name the stage, got %q", body)
	}
	if !strings.Contains(body, "permission denied") {
		t.Errorf("failure body must carry the error, got %q", body)
	}
}

func TestBuildMirroredMessageCaps(t *testing.T) {
	rep := &GitReport{}
	for i := 0; i < 120; i++ {
		rep.Mirrored = append(rep.Mirrored, MirrorEvent{
			Mirror: "m", SourceRepo: "a/b", DestRepo: "c/d", RefsPushed: 1,
		})
	}
	m := BuildMirroredMessage(rep, false)
	body := strings.Join(m.Sections[0].Lines, "\n")
	if !strings.Contains(body, "more") {
		t.Errorf("expected a truncation marker for a large report")
	}
	if len(m.Sections[0].Lines) > maxNotifyLines+1 {
		t.Errorf("got %d lines, want at most %d plus the marker", len(m.Sections[0].Lines), maxNotifyLines)
	}
}

func successColor() string {
	return BuildMirroredMessage(&GitReport{}, false).Color
}

func TestBuildMirroredMessageDetailedCaps(t *testing.T) {
	rep := &GitReport{}
	// Two mirrors, 40 repos each, so the cap falls inside the second group.
	for _, mirror := range []string{"first", "second"} {
		for i := 0; i < 40; i++ {
			rep.Mirrored = append(rep.Mirrored, MirrorEvent{
				Mirror:     mirror,
				SourceRepo: fmt.Sprintf("acme/%s-%d", mirror, i),
				DestRepo:   fmt.Sprintf("mirrors/%s-%d", mirror, i),
				RefsPushed: 1,
			})
		}
	}

	m := BuildMirroredMessage(rep, true)

	total := 0
	for _, sec := range m.Sections {
		total += len(sec.Lines)
	}
	// maxNotifyLines rendered lines plus one line for the overflow marker.
	if total != maxNotifyLines+1 {
		t.Errorf("rendered %d lines, want %d plus the overflow marker", total, maxNotifyLines)
	}

	last := m.Sections[len(m.Sections)-1]
	if last.Heading != "" {
		t.Errorf("the overflow marker must sit in its own unheaded section, got heading %q", last.Heading)
	}
	if len(last.Lines) != 1 || !strings.Contains(last.Lines[0], "30 more") {
		t.Errorf("overflow marker wrong: %+v", last.Lines)
	}

	// The first group fits whole, the second is cut short and must say so.
	if m.Sections[0].Heading != "first (40)" {
		t.Errorf("first heading = %q, want first (40)", m.Sections[0].Heading)
	}
	if m.Sections[1].Heading != "second (10 of 40)" {
		t.Errorf("second heading = %q, want second (10 of 40)", m.Sections[1].Heading)
	}
}

func TestBuildMessagesWithNothingToReport(t *testing.T) {
	empty := &GitReport{}
	for _, detailed := range []bool{false, true} {
		if got := BuildMirroredMessage(empty, detailed); len(got.Sections) != 0 {
			t.Errorf("detailed=%v: an empty report must produce no sections, got %+v", detailed, got.Sections)
		}
		if got := BuildFailureMessage(empty, detailed); len(got.Sections) != 0 {
			t.Errorf("detailed=%v: an empty failure report must produce no sections, got %+v", detailed, got.Sections)
		}
	}
}
