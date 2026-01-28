package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	Version   int                                       `json:"version"`
	UpdatedAt time.Time                                 `json:"updated_at"`
	Synced    map[string]map[string]map[string]time.Time `json:"synced"` // destName -> image -> tag -> time
}

func New() *State {
	return &State{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Synced:    make(map[string]map[string]map[string]time.Time),
	}
}

func Load(path string) (*State, error) {
	if path == "" {
		return New(), nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	if len(b) == 0 {
		return New(), nil
	}

	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if s.Synced == nil {
		s.Synced = make(map[string]map[string]map[string]time.Time)
	}
	return &s, nil
}

func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	s.UpdatedAt = time.Now().UTC()

	dir := filepath.Dir(path)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create state dir: %w", err)
		}
	}

	tmp := path + ".tmp"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func (s *State) IsSynced(destName, image, tag string) bool {
	if s == nil {
		return false
	}
	if s.Synced == nil {
		return false
	}
	if s.Synced[destName] == nil {
		return false
	}
	if s.Synced[destName][image] == nil {
		return false
	}
	_, ok := s.Synced[destName][image][tag]
	return ok
}

func (s *State) MarkSynced(destName, image, tag string) {
	if s.Synced == nil {
		s.Synced = make(map[string]map[string]map[string]time.Time)
	}
	if s.Synced[destName] == nil {
		s.Synced[destName] = make(map[string]map[string]time.Time)
	}
	if s.Synced[destName][image] == nil {
		s.Synced[destName][image] = make(map[string]time.Time)
	}
	s.Synced[destName][image][tag] = time.Now().UTC()
}

