package gitsync

import (
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
