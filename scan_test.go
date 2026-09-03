package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// found is every path scan handed to the callback, relative to root.
func found(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	scan(root, func(path string) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		got = append(got, rel)
	})
	sort.Strings(got)
	return got
}

// mkfile creates a file, making any parent directories it needs.
func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("book"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The reason recursion exists: a library is nested. Shelfarr writes
// Author/Title/book.epub, and a flat read finds none of it.
func TestScanFindsNestedFiles(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "Adrian Tchaikovsky/Service Model/Service Model.epub")

	got := found(t, root)

	if len(got) != 1 || got[0] != "Adrian Tchaikovsky/Service Model/Service Model.epub" {
		t.Errorf("nested file was not found: %v", got)
	}
}

// A flat directory is the drop-folder case, and must still work: recursion
// over one level finds exactly what a single read would.
func TestScanFindsFilesAtTheRoot(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "book.epub")

	if got := found(t, root); len(got) != 1 || got[0] != "book.epub" {
		t.Errorf("a file at the root was not found: %v", got)
	}
}

func TestScanFindsFilesAtEveryDepth(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "loose.epub")
	mkfile(t, root, "Author/book.epub")
	mkfile(t, root, "Author/Title/book.epub")

	got := found(t, root)

	if len(got) != 3 {
		t.Errorf("expected 3 files at mixed depths, got %v", got)
	}
}

// Hidden directories are bookkeeping -- Shelfarr's .shelfarr-staging-v2,
// Syncthing's .stfolder -- and a partial download in one is not a book.
func TestScanSkipsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".shelfarr-staging-v2/direct-downloads/partial.epub")
	mkfile(t, root, "real.epub")

	got := found(t, root)

	if len(got) != 1 || got[0] != "real.epub" {
		t.Errorf("a hidden directory was walked into: %v", got)
	}
}

// And hidden files: a half-written .book.epub.partial is not something to
// email.
func TestScanSkipsHiddenFiles(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".book.epub.partial")
	mkfile(t, root, "real.epub")

	if got := found(t, root); len(got) != 1 || got[0] != "real.epub" {
		t.Errorf("a hidden file was handed on: %v", got)
	}
}

// Directories are not files: handing one to the sender would be a read error
// at best.
func TestScanDoesNotHandOnDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Empty Author"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := found(t, root); len(got) != 0 {
		t.Errorf("a directory was handed on: %v", got)
	}
}

func TestScanOfAnEmptyTreeFindsNothing(t *testing.T) {
	if got := found(t, t.TempDir()); len(got) != 0 {
		t.Errorf("an empty tree produced %v", got)
	}
}

// A missing directory must not panic or exit: the mount may not be there yet
// on first start, and the next scan will find it.
func TestScanOfAMissingDirectoryIsNotFatal(t *testing.T) {
	got := found(t, filepath.Join(t.TempDir(), "not-there"))

	if len(got) != 0 {
		t.Errorf("a missing directory produced %v", got)
	}
}

// --- SCAN_INTERVAL ---

func TestIntervalDefaultsWhenUnset(t *testing.T) {
	got, err := parseInterval("")
	if err != nil {
		t.Fatalf("unset should be valid: %v", err)
	}
	if got != defaultInterval {
		t.Errorf("expected %s, got %s", defaultInterval, got)
	}
}

func TestIntervalAcceptsADuration(t *testing.T) {
	got, err := parseInterval("30s")
	if err != nil {
		t.Fatalf("30s should be valid: %v", err)
	}
	if got != 30*time.Second {
		t.Errorf("expected 30s, got %s", got)
	}
}

// The counterpart: nonsense must be refused rather than silently defaulted,
// or a typo becomes a scan interval nobody chose.
func TestIntervalRejectsNonsense(t *testing.T) {
	if _, err := parseInterval("soon"); err == nil {
		t.Error("a non-duration was accepted")
	}
}

// A one-second scan of a real library is a stat storm that finds nothing.
// Refused rather than quietly raised, so the misunderstanding surfaces.
func TestIntervalRejectsTooShort(t *testing.T) {
	if _, err := parseInterval("1s"); err == nil {
		t.Error("an interval below the floor was accepted")
	}
}
