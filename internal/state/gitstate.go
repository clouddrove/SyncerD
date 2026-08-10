package state

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// RepoState records the last successful mirror of one source repository.
type RepoState struct {
	Fingerprint string    `json:"fingerprint"`
	DestPath    string    `json:"dest_path"`
	SyncedAt    time.Time `json:"synced_at"`
}

// GitState tracks mirrored repositories. It lives in its own file so a
// corrupt git state cannot break image sync.
type GitState struct {
	Version   int                             `json:"version"`
	UpdatedAt time.Time                       `json:"updated_at"`
	Mirrored  map[string]map[string]RepoState `json:"mirrored"` // mirror -> source repo path -> state
}

// NewGit returns an empty git state.
func NewGit() *GitState {
	return &GitState{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Mirrored:  make(map[string]map[string]RepoState),
	}
}

// LoadGit reads a git state file. A missing or empty file is not an error.
func LoadGit(path string) (*GitState, error) {
	if path == "" {
		return NewGit(), nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewGit(), nil
		}
		return nil, fmt.Errorf("read git state file: %w", err)
	}
	if len(b) == 0 {
		return NewGit(), nil
	}

	var s GitState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse git state file: %w", err)
	}
	if s.Mirrored == nil {
		s.Mirrored = make(map[string]map[string]RepoState)
	}
	return &s, nil
}

// Save writes the git state atomically.
func (s *GitState) Save(path string) error {
	if path == "" {
		return nil
	}
	s.UpdatedAt = time.Now().UTC()
	return writeAtomic(path, s)
}

// Get returns the recorded state for a source repository under a mirror.
func (s *GitState) Get(mirror, repoPath string) (RepoState, bool) {
	if s == nil || s.Mirrored == nil {
		return RepoState{}, false
	}
	byRepo, ok := s.Mirrored[mirror]
	if !ok {
		return RepoState{}, false
	}
	rs, ok := byRepo[repoPath]
	return rs, ok
}

// Mark records a successful mirror.
func (s *GitState) Mark(mirror, repoPath, destPath, fingerprint string) {
	if s.Mirrored == nil {
		s.Mirrored = make(map[string]map[string]RepoState)
	}
	if s.Mirrored[mirror] == nil {
		s.Mirrored[mirror] = make(map[string]RepoState)
	}
	s.Mirrored[mirror][repoPath] = RepoState{
		Fingerprint: fingerprint,
		DestPath:    destPath,
		SyncedAt:    time.Now().UTC(),
	}
}
