package commands

import (
	"fmt"
	"strings"
	"time"

	"wharf/internal/logging"
	"wharf/internal/sftpclient"
)

// remoteEntry is one file or directory discovered while walking a remote
// SFTP tree. relPath is relative to whatever root was passed to
// walkRemoteTree (slash-separated, no leading slash); the root itself is
// included with relPath == "".
type remoteEntry struct {
	relPath string
	isDir   bool
	size    int64
	modTime time.Time
}

// walkRemoteTree walks the remote directory tree rooted at root
// (inclusive of root itself) and returns every entry beneath it. Errors
// encountered on individual entries are logged (if logger is non-nil) and
// skipped rather than aborting the whole walk, so one bad entry doesn't
// hide the rest of the tree from the caller.
// Progress is printed straight to stdout (a single line, overwritten with
// \r) purely for visual feedback on large trees -- it never affects what
// gets written to the log file, which still records every entry via
// logger.Step below regardless of what's shown on screen.
func walkRemoteTree(client *sftpclient.Client, root string, logger *logging.Logger) ([]remoteEntry, error) {
	var entries []remoteEntry
	count := 0

	walker := client.SFTP.Walk(root)
	for walker.Step() {
		if werr := walker.Err(); werr != nil {
			if logger != nil {
				logger.Error("error while walking %s: %v", walker.Path(), werr)
			}
			continue
		}
		p := walker.Path()
		info := walker.Stat()
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		isDir := info.IsDir()
		entries = append(entries, remoteEntry{relPath: rel, isDir: isDir, size: info.Size(), modTime: info.ModTime()})
		count++
		fmt.Printf("\rWalking remote tree: %d entries found so far (%s)\033[K", count, p)
		if logger != nil {
			kind := "file"
			if isDir {
				kind = "dir"
			}
			logger.Step("visited %s: %s (size=%d)", kind, p, info.Size())
		}
	}
	if count > 0 {
		fmt.Println()
	}

	return entries, nil
}
