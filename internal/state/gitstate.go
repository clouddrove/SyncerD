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

// PRRecord is what a run needs to recognise a destination pull request it
// created earlier, and to decide whether anything changed since.
//
// This is an optimisation, never the truth. Every decision it drives is
// recoverable from the destination itself through the marker SyncerD writes
// into each body, so a lost or deleted state file causes re-inspection, not
// a duplicate pull request.
type PRRecord struct {
	DestNumber    int               `json:"dest_number"`
	DestState     string            `json:"dest_state"`
	SourceUpdated time.Time         `json:"source_updated"`
	CommentIDs    map[string]string `json:"comment_ids,omitempty"` // source comment id -> destination comment id
}

// GitState tracks mirrored repositories. It lives in its own file so a
// corrupt git state cannot break image sync.
type GitState struct {
	Version   int                             `json:"version"`
	UpdatedAt time.Time                       `json:"updated_at"`
	Mirrored  map[string]map[string]RepoState `json:"mirrored"` // mirror -> source repo path -> state
	// PullRequests is mirror -> source repo path -> source pull request
	// number -> record. Absent from a file written before pull request
	// mirroring existed, which loads as an empty map rather than an error.
	PullRequests map[string]map[string]map[int]PRRecord `json:"pull_requests,omitempty"`
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

// GetPR returns the record for one mirrored pull request.
func (s *GitState) GetPR(mirror, repoPath string, number int) (PRRecord, bool) {
	if s == nil || s.PullRequests == nil {
		return PRRecord{}, false
	}
	byRepo, ok := s.PullRequests[mirror]
	if !ok {
		return PRRecord{}, false
	}
	byNumber, ok := byRepo[repoPath]
	if !ok {
		return PRRecord{}, false
	}
	rec, ok := byNumber[number]
	return rec, ok
}

// MarkPR records the outcome of mirroring one pull request.
func (s *GitState) MarkPR(mirror, repoPath string, number int, rec PRRecord) {
	if s.PullRequests == nil {
		s.PullRequests = make(map[string]map[string]map[int]PRRecord)
	}
	if s.PullRequests[mirror] == nil {
		s.PullRequests[mirror] = make(map[string]map[int]PRRecord)
	}
	if s.PullRequests[mirror][repoPath] == nil {
		s.PullRequests[mirror][repoPath] = make(map[int]PRRecord)
	}
	s.PullRequests[mirror][repoPath][number] = rec
}

// ForgetPR drops a record, so the next run re-inspects the destination.
func (s *GitState) ForgetPR(mirror, repoPath string, number int) {
	if s == nil || s.PullRequests == nil {
		return
	}
	if byRepo, ok := s.PullRequests[mirror]; ok {
		if byNumber, ok := byRepo[repoPath]; ok {
			delete(byNumber, number)
		}
	}
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
