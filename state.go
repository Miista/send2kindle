package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// State records what has already been decided about a file, so a decision is
// made once rather than every time the tool looks.
//
// In drop mode the absence of the file IS the record: it is sent and removed,
// and what is gone is not reconsidered. A library cannot work that way -- the
// book stays -- so something has to remember, and without it every restart
// would re-send the whole shelf.
//
// It records outcomes, not successes. An oversized book that cannot be sent is
// a decision too: recording it is what stops the warning firing on every
// restart, which is how a 115MB epub logged the same error for eighteen days
// and nobody saw any of them.
type State struct {
	path string

	mu      sync.Mutex
	Entries map[string]Entry `json:"entries"`
}

// Entry is what happened to one file, and why.
//
// Outcome is kept for the reader rather than the code -- the state file is
// something a person opens when a book did not arrive, and "sent" against a
// title answers the question that brought them there.
type Entry struct {
	Outcome string `json:"outcome"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
}

// Outcomes a file can reach. Each is terminal: reaching one means the file is
// not looked at again.
const (
	OutcomeSent      = "sent"
	OutcomeTooLarge  = "too_large"
	OutcomeUnusable  = "unsupported_format"
	OutcomeSendError = "send_failed"
)

// LoadState reads the state file, or starts an empty one if there is none.
//
// A missing file is not an error: the first run has nothing to remember. A
// CORRUPT one is also not an error, deliberately -- refusing to start because
// the record is unreadable would be worse than re-sending, and re-sending is
// the failure mode a person can see and fix.
func LoadState(path string) *State {
	s := &State{path: path, Entries: map[string]Entry{}}

	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var loaded State
	if err := json.Unmarshal(b, &loaded); err != nil {
		return s
	}
	if loaded.Entries != nil {
		s.Entries = loaded.Entries
	}
	return s
}

// Done reports whether this file already reached an outcome.
func (s *State) Done(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Entries[key]
	return ok
}

// Record stores an outcome and writes the file.
//
// Written on every record rather than at exit: the tool is a long-running
// container that is stopped by being killed, so anything held in memory until
// shutdown is anything lost.
func (s *State) Record(key, outcome, name string, size int64) error {
	s.mu.Lock()
	s.Entries[key] = Entry{Outcome: outcome, Name: name, Size: size}
	s.mu.Unlock()
	return s.save()
}

// save writes the state file atomically: a write interrupted part way through
// would leave a file that parses as empty, and an empty state re-sends the
// shelf.
func (s *State) save() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, s.path)
}
