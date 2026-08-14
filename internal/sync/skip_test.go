package sync

import (
	"context"
	"testing"

	"github.com/clouddrove/syncerd/internal/config"
	"github.com/clouddrove/syncerd/internal/registry"
	"github.com/clouddrove/syncerd/internal/state"
)

// fakeRegistry is a minimal registry.Registry for exercising SyncTag's skip
// paths without any network access.
type fakeRegistry struct {
	url    string
	exists bool
}

func (f *fakeRegistry) Authenticate(context.Context) error                 { return nil }
func (f *fakeRegistry) ListTags(context.Context, string) ([]string, error) { return nil, nil }
func (f *fakeRegistry) ImageExists(context.Context, string, string) (bool, error) {
	return f.exists, nil
}
func (f *fakeRegistry) GetRegistryURL() string { return f.url }

// TestSyncTagSkipsAlreadySyncedByState covers the "Already synced (state)"
// skip in SyncTag: a tag already recorded in state must not be recopied,
// and the skip must be counted so --report does not claim nothing was
// skipped.
func TestSyncTagSkipsAlreadySyncedByState(t *testing.T) {
	cfg := &config.Config{
		Destinations: []config.DestinationConfig{{Name: "ecr"}},
	}
	st := state.New()
	st.MarkSynced("ecr", "library/nginx", "1.25")

	s := &Syncer{
		config:         cfg,
		sourceRegistry: &fakeRegistry{url: "docker.io"},
		destRegistries: []registry.Registry{&fakeRegistry{url: "ecr.example.com"}},
		state:          st,
		currentReport:  &Report{},
	}

	if err := s.SyncTag(context.Background(), "library/nginx", "1.25", config.ImageConfig{}); err != nil {
		t.Fatalf("SyncTag: %v", err)
	}
	if s.currentReport.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", s.currentReport.Skipped)
	}
	if len(s.currentReport.NewSyncs) != 0 {
		t.Errorf("expected no new syncs, got %+v", s.currentReport.NewSyncs)
	}
}

// TestSyncTagSkipsWhenImageAlreadyExistsAtDestination covers the "already
// exists in %s, skipping" path: the destination already has the image, so
// SyncTag marks state and skips the copy without an error, and that skip
// must also be counted.
func TestSyncTagSkipsWhenImageAlreadyExistsAtDestination(t *testing.T) {
	cfg := &config.Config{
		Destinations: []config.DestinationConfig{{Name: "ecr"}},
	}
	s := &Syncer{
		config:         cfg,
		sourceRegistry: &fakeRegistry{url: "docker.io"},
		destRegistries: []registry.Registry{&fakeRegistry{url: "ecr.example.com", exists: true}},
		state:          state.New(),
		currentReport:  &Report{},
	}

	if err := s.SyncTag(context.Background(), "library/nginx", "1.25", config.ImageConfig{}); err != nil {
		t.Fatalf("SyncTag: %v", err)
	}
	if s.currentReport.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", s.currentReport.Skipped)
	}
	if !s.state.IsSynced("ecr", "library/nginx", "1.25") {
		t.Error("expected state to be marked synced after the exists check")
	}
}
