package main

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// fileKey identifies a file for the state record.
//
// Inode and size, not path: a library renames files. Metadata refreshes,
// author corrections and edition changes all rewrite the path of a book that
// is unchanged, and a path-keyed record would call each one a new book and
// send it again.
//
// Inode is also the honest key for the library this was built for, where
// qBittorrent and the shelf hold hardlinks to the same bytes -- two paths for
// one inode really is one book.
//
// Size is included because inode numbers are reused: a file deleted and
// another created can land on the same inode, and size makes that collision
// visible instead of silently marking the new file as already handled.
func fileKey(info fs.FileInfo) (string, error) {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("no inode available for %s", info.Name())
	}
	return fmt.Sprintf("%d-%d", sys.Ino, info.Size()), nil
}

// keyOf is fileKey for a path.
func keyOf(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fileKey(info)
}
