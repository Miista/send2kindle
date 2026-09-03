package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseModeDefaultsToDrop(t *testing.T) {
	// Unset must mean drop: a new setting should not change what an existing
	// deployment does when it restarts.
	got, err := parseMode("")
	if err != nil {
		t.Fatalf("empty WATCH_MODE should be valid: %v", err)
	}
	if got != ModeDrop {
		t.Errorf("expected drop, got %q", got)
	}
}

func TestParseModeAcceptsLibrary(t *testing.T) {
	got, err := parseMode("library")
	if err != nil {
		t.Fatalf("library should be valid: %v", err)
	}
	if got != ModeLibrary {
		t.Errorf("expected library, got %q", got)
	}
}

func TestParseModeIsCaseInsensitive(t *testing.T) {
	if got, err := parseMode("LIBRARY"); err != nil || got != ModeLibrary {
		t.Errorf("expected library from LIBRARY, got %q (%v)", got, err)
	}
}

// The counterpart: a typo must be refused rather than silently treated as
// drop. Falling back would mean a library quietly being consumed.
func TestParseModeRejectsAnythingElse(t *testing.T) {
	if _, err := parseMode("libary"); err == nil {
		t.Error("a misspelled mode was accepted")
	}
}

func TestStateRemembersAnOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.json")
	s := LoadState(path)

	if s.Done("1-2") {
		t.Error("an empty state claimed to know a file")
	}
	if err := s.Record("1-2", OutcomeSent, "book.epub", 2); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !s.Done("1-2") {
		t.Error("a recorded outcome was not remembered")
	}
}

// The point of the file: what was decided survives a restart.
func TestStateSurvivesAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.json")
	first := LoadState(path)
	if err := first.Record("7-99", OutcomeSent, "book.epub", 99); err != nil {
		t.Fatalf("record: %v", err)
	}

	second := LoadState(path)

	if !second.Done("7-99") {
		t.Error("the outcome was lost across a reload")
	}
	if got := second.Entries["7-99"].Name; got != "book.epub" {
		t.Errorf("the entry lost its name: %q", got)
	}
}

// A corrupt state file must not stop the tool starting. Re-sending is a
// failure a person can see and fix; refusing to run is not.
func TestStateStartsFreshWhenTheFileIsUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := LoadState(path)

	if s.Done("anything") {
		t.Error("a corrupt state file was treated as authoritative")
	}
	if err := s.Record("1-1", OutcomeSent, "book.epub", 1); err != nil {
		t.Errorf("a fresh state should still be writable: %v", err)
	}
}

// Inode-and-size means a renamed file is the same file. This is the whole
// reason the key is not the path: a library renames books on metadata
// refresh, and a path key would re-send every one of them.
func TestKeyIsStableAcrossARename(t *testing.T) {
	dir := t.TempDir()
	before := filepath.Join(dir, "Title.epub")
	if err := os.WriteFile(before, []byte("book"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := keyOf(before)
	if err != nil {
		t.Fatal(err)
	}

	after := filepath.Join(dir, "Author - Title (2024).epub")
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	keyAfter, err := keyOf(after)
	if err != nil {
		t.Fatal(err)
	}

	if keyBefore != keyAfter {
		t.Errorf("a rename changed the key: %q then %q", keyBefore, keyAfter)
	}
}

// And a hardlink is the same file too -- which is the case this was built
// for: qBittorrent seeds one link while the shelf holds another.
func TestKeyIsTheSameForAHardlink(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(original, []byte("book"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.epub")
	if err := os.Link(original, link); err != nil {
		t.Fatal(err)
	}

	a, _ := keyOf(original)
	b, _ := keyOf(link)

	if a != b {
		t.Errorf("two links to one inode produced different keys: %q and %q", a, b)
	}
}

// Different files must not collide, or one would suppress the other.
func TestKeyDiffersBetweenFiles(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.epub")
	two := filepath.Join(dir, "two.epub")
	os.WriteFile(one, []byte("a"), 0o644)
	os.WriteFile(two, []byte("bb"), 0o644)

	a, _ := keyOf(one)
	b, _ := keyOf(two)

	if a == b {
		t.Errorf("two different files share a key: %q", a)
	}
}
