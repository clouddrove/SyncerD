package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/notify"
)

// syncerWith returns a Syncer whose Slack message format is set as given.
func syncerWith(format string) *Syncer {
	return &Syncer{config: &config.Config{Slack: config.SlackConfig{MessageFormat: format}}}
}

// joinLines concatenates every section line of a Message for easy assertions.
func joinLines(m notify.Message) string {
	var b strings.Builder
	for _, sec := range m.Sections {
		if sec.Heading != "" {
			b.WriteString(sec.Heading + "\n")
		}
		for _, l := range sec.Lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

func TestRetrySucceedsAfterFailures(t *testing.T) {
	calls := 0
	err := retry(context.Background(), 3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryReturnsLastError(t *testing.T) {
	want := errors.New("boom")
	calls := 0
	err := retry(context.Background(), 2, time.Millisecond, func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected last error %v, got %v", want, err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

func TestRetryStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retry(ctx, 3, time.Millisecond, func() error {
		calls++
		return errors.New("transient")
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if calls != 0 {
		t.Fatalf("expected fn not to run on cancelled context, got %d calls", calls)
	}
}

func TestSlackCompactNewSyncs(t *testing.T) {
	events := []SyncEvent{
		{Destination: "ecr", Ref: "ecr.example.com/library/nginx:1.25"},
		{Destination: "acr", Ref: "acr.example.com/library/nginx:1.25"},
	}
	m := syncerWith("compact").buildNewSyncsMessage(events)
	if m.Color != notify.ColorSuccess {
		t.Errorf("expected success color, got %q", m.Color)
	}
	if !strings.Contains(m.Title, "2 image(s)/tag(s) synced") {
		t.Errorf("unexpected title: %q", m.Title)
	}
	if !strings.Contains(joinLines(m), "ecr.example.com/library/nginx:1.25") {
		t.Errorf("message missing ref: %q", joinLines(m))
	}
}

func TestSlackCompactNewSyncsTruncates(t *testing.T) {
	var events []SyncEvent
	for i := 0; i < 60; i++ {
		events = append(events, SyncEvent{Ref: "ref"})
	}
	m := syncerWith("compact").buildNewSyncsMessage(events)
	if !strings.Contains(joinLines(m), "and 10 more") {
		t.Errorf("expected truncation note, got: %q", joinLines(m))
	}
}

func TestSlackCompactFailures(t *testing.T) {
	events := []FailureEvent{
		{Destination: "ecr", Ref: "ecr.example.com/library/nginx:1.25", Error: "denied"},
	}
	m := syncerWith("compact").buildFailureMessage(events)
	if m.Color != notify.ColorFailure {
		t.Errorf("expected failure color, got %q", m.Color)
	}
	body := joinLines(m)
	if !strings.Contains(body, "denied") || !strings.Contains(m.Title, "1 sync failure(s)") {
		t.Errorf("failure message malformed: title=%q body=%q", m.Title, body)
	}
}

func TestSlackDetailedGroupsByDestination(t *testing.T) {
	events := []SyncEvent{
		{Destination: "ecr", Ref: "a"},
		{Destination: "ecr", Ref: "b"},
		{Destination: "acr", Ref: "c"},
	}
	m := syncerWith("detailed").buildNewSyncsMessage(events)
	body := joinLines(m)
	if !strings.Contains(body, "ecr (2)") {
		t.Errorf("expected ecr group header, got: %q", body)
	}
	if !strings.Contains(body, "acr (1)") {
		t.Errorf("expected acr group header, got: %q", body)
	}
}
