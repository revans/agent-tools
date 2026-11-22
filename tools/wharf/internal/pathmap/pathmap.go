// Package pathmap converts between local filesystem paths (relative to the
// site repo root) and the corresponding remote SFTP paths
// (relative to the configured remote base path).
package pathmap

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// RepoRelative resolves localPath (which may be absolute or relative to the
// current working directory) to a slash-separated path relative to
// repoRoot. It returns an error if localPath does not live inside repoRoot.
func RepoRelative(repoRoot, localPath string) (string, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repo root %s: %w", repoRoot, err)
	}
	absLocal, err := filepath.Abs(localPath)
	if err != nil {
		return "", fmt.Errorf("resolving local path %s: %w", localPath, err)
	}

	rel, err := filepath.Rel(absRoot, absLocal)
	if err != nil {
		return "", fmt.Errorf("computing relative path from %s to %s: %w", absRoot, absLocal, err)
	}
	if rel == "." {
		return "", fmt.Errorf("%s is the repo root itself, not a file inside it", localPath)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the repo root %s", localPath, absRoot)
	}

	return filepath.ToSlash(rel), nil
}

// RemotePath joins the configured remote base path with a repo-relative
// path (as produced by RepoRelative) to produce the full remote SFTP path.
// It always returns a clean, forward-slash path.
func RemotePath(remoteBasePath, repoRelPath string) string {
	base := strings.TrimRight(remoteBasePath, "/")
	if base == "" {
		base = "/"
	}
	rel := strings.TrimLeft(filepath.ToSlash(repoRelPath), "/")
	return path.Join(base, rel)
}
