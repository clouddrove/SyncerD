package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
	msg := slackCompactNewSyncs(events)
	if !strings.Contains(msg, "ecr.example.com/library/nginx:1.25") {
		t.Errorf("compact message missing ref: %q", msg)
	}
	if !strings.Contains(msg, "new images/tags synced") {
		t.Errorf("compact message missing header: %q", msg)
	}
}

func TestSlackCompactNewSyncsTruncates(t *testing.T) {
	var events []SyncEvent
	for i := 0; i < 40; i++ {
		events = append(events, SyncEvent{Ref: "ref"})
	}
	msg := slackCompactNewSyncs(events)
	if !strings.Contains(msg, "and 15 more") {
		t.Errorf("expected truncation note, got: %q", msg)
	}
}

func TestSlackCompactFailures(t *testing.T) {
	events := []FailureEvent{
		{Destination: "ecr", Ref: "ecr.example.com/library/nginx:1.25", Error: "denied"},
	}
	msg := slackCompactFailures(events)
	if !strings.Contains(msg, "denied") || !strings.Contains(msg, "sync failures") {
		t.Errorf("failure message malformed: %q", msg)
	}
}

func TestSlackDetailedGroupsByDestination(t *testing.T) {
	events := []SyncEvent{
		{Destination: "ecr", Ref: "a"},
		{Destination: "ecr", Ref: "b"},
		{Destination: "acr", Ref: "c"},
	}
	msg := slackDetailedNewSyncs(events)
	if !strings.Contains(msg, "*ecr* (2)") {
		t.Errorf("expected ecr group header, got: %q", msg)
	}
	if !strings.Contains(msg, "*acr* (1)") {
		t.Errorf("expected acr group header, got: %q", msg)
	}
}
