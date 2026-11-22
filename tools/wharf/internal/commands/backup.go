package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wharf/internal/logging"
	"wharf/internal/sftpclient"
)

// backupRetention is how many backup directories are kept per distinct
// remote path before older ones are pruned. Hardcoded for v1 per design
// decision -- not worth making configurable yet.
const backupRetention = 5

// backupTimestampLayout is used for both the backup directory prefix and
// pruneOldBackups' fixed-width parsing of it below.
const backupTimestampLayout = "20060102-150405"

// flattenRemotePath turns a remote path into a filesystem-safe name by
// stripping leading/trailing slashes and replacing the rest with hyphens,
// e.g. "/assets/foo/bar.png" -> "assets-foo-bar.png".
func flattenRemotePath(remotePath string) string {
	return strings.ReplaceAll(strings.Trim(remotePath, "/"), "/", "-")
}

// newBackupDir creates and returns a fresh timestamped backup directory
// for remotePath under scripts/wharf/backups/, following the
// <timestamp>-<flattened-remote-path>/ convention.
func newBackupDir(repoRoot, remotePath string) (string, error) {
	ts := time.Now().Format(backupTimestampLayout)
	flattened := flattenRemotePath(remotePath)
	dir := filepath.Join(repoRoot, "scripts", "wharf", "backups", fmt.Sprintf("%s-%s", ts, flattened))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup directory %s: %w", dir, err)
	}
	return dir, nil
}

// backupRemoteFile downloads the current remote file at remotePath (if one
// exists) into a new timestamped backup directory before it gets
// overwritten by copy/sync. Returns the local backup file path, or "" if
// there was nothing remote to back up yet (a brand-new upload). It also
// prunes old backups for this remote path down to backupRetention.
func backupRemoteFile(client *sftpclient.Client, logger *logging.Logger, repoRoot, remotePath string) (string, error) {
	info, err := client.SFTP.Stat(remotePath)
	if err != nil {
		logger.Step("no existing remote file at %s; nothing to back up before upload", remotePath)
		return "", nil
	}
	if info.IsDir() {
		return "", fmt.Errorf("remote path %s is a directory; refusing to overwrite it with a file upload", remotePath)
	}

	backupDir, err := newBackupDir(repoRoot, remotePath)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(backupDir, filepath.Base(remotePath))
	if err := downloadFile(client, logger, remotePath, dest); err != nil {
		return "", err
	}
	logger.Step("backed up existing remote file %s -> %s before overwrite", remotePath, dest)

	if err := pruneOldBackups(repoRoot, flattenRemotePath(remotePath), logger); err != nil {
		// Pruning failure shouldn't block the upload itself -- just log it.
		logger.Error("failed pruning old backups for %s: %v", remotePath, err)
	}

	return dest, nil
}

// pruneOldBackups keeps only the backupRetention most recent backup
// directories matching "<timestamp>-<flattened>" for a given flattened
// remote path, deleting older ones. Since backupTimestampLayout produces a
// fixed-width, lexically-sortable prefix, a plain string sort of matching
// directory names gives chronological order.
func pruneOldBackups(repoRoot, flattened string, logger *logging.Logger) error {
	backupsRoot := filepath.Join(repoRoot, "scripts", "wharf", "backups")
	entries, err := os.ReadDir(backupsRoot)
	if err != nil {
		return fmt.Errorf("reading backups directory %s: %w", backupsRoot, err)
	}

	tsLen := len(backupTimestampLayout)
	var matches []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) <= tsLen+1 {
			continue
		}
		// name is "<timestamp>-<flattened>"; only compare the exact
		// remainder after the fixed-width timestamp + separator, so one
		// flattened path can't accidentally match as a suffix of another.
		if name[tsLen] != '-' {
			continue
		}
		if name[tsLen+1:] == flattened {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)

	if len(matches) <= backupRetention {
		return nil
	}

	toDelete := matches[:len(matches)-backupRetention]
	for _, name := range toDelete {
		p := filepath.Join(backupsRoot, name)
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("removing old backup %s: %w", p, err)
		}
		if logger != nil {
			logger.Step("pruned old backup %s (retention limit: %d per remote path)", p, backupRetention)
		}
	}
	return nil
}

// downloadFile streams a single remote file down to a local path, creating
// parent directories as needed. Shared by the copy/sync pre-overwrite
// backup path and wharf delete's backup-before-delete path.
func downloadFile(client *sftpclient.Client, logger *logging.Logger, remotePath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("creating local directory for %s: %w", localPath, err)
	}

	src, err := client.SFTP.Open(remotePath)
	if err != nil {
		logger.Error("failed to open remote file %s for backup: %v", remotePath, err)
		return fmt.Errorf("opening remote file %s: %w", remotePath, err)
	}
	defer src.Close()

	dst, err := os.Create(localPath)
	if err != nil {
		logger.Error("failed to create local backup file %s: %v", localPath, err)
		return fmt.Errorf("creating local backup file %s: %w", localPath, err)
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		logger.Error("failed downloading %s to %s: %v", remotePath, localPath, err)
		return fmt.Errorf("downloading %s to %s: %w", remotePath, localPath, err)
	}
	logger.Step("downloaded %s -> %s (%d bytes)", remotePath, localPath, n)
	return nil
}
