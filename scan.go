package main

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
)

// scan walks the watched tree and hands every file to fn.
//
// Recursive in both modes, deliberately. A drop folder is flat, so walking it
// finds exactly what a single ReadDir would -- and a frontend that starts
// writing `Author/Title/book.epub` into one is then handled rather than
// silently ignored, which is the failure that would otherwise be invisible.
//
// Errors reading a subtree are logged and skipped rather than returned: one
// unreadable directory should not stop the rest of a library being seen.
func scan(root string, fn func(path string)) {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("skipping unreadable path", "path", path, "error", err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Hidden directories are the tool's own bookkeeping and the
			// frontend's: .shelfarr-staging, .stfolder, and the partial files
			// this tool's own Drop helper writes. Nothing in them is a book.
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		fn(path)
		return nil
	})
	if err != nil {
		slog.Error("walking the watch directory", "dir", root, "error", err)
	}
}
