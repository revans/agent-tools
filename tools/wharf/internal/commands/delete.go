package commands

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"wharf/internal/config"
	"wharf/internal/logging"
	"wharf/internal/pathmap"
	"wharf/internal/sftpclient"
)

// Delete removes a file or directory on the remote SFTP server. This is
// the single most destructive command wharf has, so it always shows the
// full blast radius before asking anything, always requires an explicit
// "y"/"yes" confirmation (no flag can skip this), and offers to download a
// backup before it deletes anything for real.
//
// remoteArg is interpreted the same way WHARF.md lists remote paths: it is
// relative to the configured remote base path, not the absolute remote
// filesystem path.
//
// If dryRun is true, wharf still connects and inspects the target (so it
// can show an accurate blast radius) but stops before any confirmation
// prompt, backup, or deletion.
func Delete(cfg *config.Config, repoRoot, remoteArg string, dryRun bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	remotePath := pathmap.RemotePath(cfg.RemoteBasePath, remoteArg)
	logger.Step("delete target resolved to remote path %s (from argument %q)", remotePath, remoteArg)

	root := normalizeRemoteRoot(cfg.RemoteBasePath)
	if remotePath == root {
		err := fmt.Errorf("refusing to delete the remote base path itself (%s); pass a path inside it", root)
		logger.Error("%v", err)
		return err
	}

	logger.Step("connecting to %s:%d as %s", cfg.Host, cfg.Port, cfg.Username)
	client, err := sftpclient.Connect(cfg)
	if err != nil {
		logger.Error("connection failed: %v", err)
		return fmt.Errorf("connecting: %w", err)
	}
	defer client.Close()
	logger.Step("connected successfully")

	info, err := client.SFTP.Stat(remotePath)
	if err != nil {
		logger.Error("cannot stat remote target %s: %v", remotePath, err)
		return fmt.Errorf("remote target %s does not exist or is not accessible: %w", remotePath, err)
	}

	isDir := info.IsDir()
	var children []remoteEntry
	if isDir {
		all, err := walkRemoteTree(client, remotePath, logger)
		if err != nil {
			logger.Error("failed walking remote directory %s: %v", remotePath, err)
			return fmt.Errorf("walking remote directory %s: %w", remotePath, err)
		}
		for _, e := range all {
			if e.relPath == "" {
				continue // the target directory itself, not its contents
			}
			children = append(children, e)
		}
	}

	fileCount, dirCount, totalSize := 0, 0, int64(0)
	if isDir {
		for _, c := range children {
			if c.isDir {
				dirCount++
			} else {
				fileCount++
				totalSize += c.size
			}
		}
	} else {
		totalSize = info.Size()
	}

	printBlastRadius(out, remotePath, isDir, fileCount, dirCount, totalSize, children)
	logger.Step("blast radius for %s: isDir=%v files=%d dirs=%d totalSizeBytes=%d", remotePath, isDir, fileCount, dirCount, totalSize)

	if dryRun {
		fmt.Fprintln(out, "\n[dry-run] stopping before confirmation/backup/delete. Nothing was changed.")
		logger.Step("[dry-run] stopping before confirmation/backup/delete; no changes made")
		return nil
	}

	reader := bufio.NewReader(in)

	confirmed, err := promptConfirmDangerous(reader, out, fmt.Sprintf("\nDelete %s? This cannot be undone.", remotePath))
	if err != nil {
		logger.Error("confirmation prompt failed: %v", err)
		return err
	}
	logger.Step("delete confirmation for %s: %v", remotePath, confirmed)
	if !confirmed {
		fmt.Fprintln(out, "Aborted. Nothing was deleted.")
		logger.Step("user declined deletion; nothing deleted")
		return nil
	}

	backupPath, err := maybeBackupBeforeDelete(client, reader, out, logger, repoRoot, remotePath, isDir, children)
	if err != nil {
		logger.Error("backup step failed: %v", err)
		return err
	}

	logger.Step("deleting %s (recursive=%v, %d total path(s))", remotePath, isDir, 1+len(children))
	deletedPaths, err := deleteRemoteTree(client, logger, out, remotePath, isDir, children)
	if err != nil {
		logger.Error("delete failed for %s: %v", remotePath, err)
		return fmt.Errorf("deleting %s: %w", remotePath, err)
	}
	logger.Step("deleted %d path(s): %s", len(deletedPaths), strings.Join(deletedPaths, ", "))

	deletedCount := len(deletedPaths)
	logger.Step("deleted %s successfully (%d total remote path(s) removed)", remotePath, deletedCount)
	fmt.Fprintf(out, "Deleted %s (%d total remote path(s) removed).\n", remotePath, deletedCount)
	if backupPath != "" {
		logger.Step("backup available at: %s", backupPath)
		fmt.Fprintf(out, "Backup saved at: %s\n", backupPath)
	}
	return nil
}

func printBlastRadius(out io.Writer, remotePath string, isDir bool, fileCount, dirCount int, totalSize int64, children []remoteEntry) {
	if !isDir {
		fmt.Fprintf(out, "About to delete remote FILE: %s (%d bytes)\n", remotePath, totalSize)
		return
	}
	fmt.Fprintf(out, "About to delete remote DIRECTORY: %s\n", remotePath)
	fmt.Fprintf(out, "  Contains: %d file(s), %d subdirectory(ies), %d bytes total\n", fileCount, dirCount, totalSize)
	if len(children) == 0 {
		return
	}
	sorted := make([]remoteEntry, len(children))
	copy(sorted, children)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].relPath < sorted[j].relPath })
	fmt.Fprintln(out, "  Contents:")
	for _, c := range sorted {
		kind := "file"
		if c.isDir {
			kind = "dir "
		}
		fmt.Fprintf(out, "    [%s] %s\n", kind, c.relPath)
	}
}

// deleteRemoteTree removes remotePath from the server, printing a
// progress line per item and logging each removal individually so the
// log file ends up with the complete, explicit list of what was deleted
// -- not just a count. For a directory, children are removed bottom-up
// (deepest paths first) since SFTP servers generally require a directory
// to be empty before it can be removed.
func deleteRemoteTree(client *sftpclient.Client, logger *logging.Logger, out io.Writer, remotePath string, isDir bool, children []remoteEntry) ([]string, error) {
	if !isDir {
		fmt.Fprintf(out, "Deleting 1/1: %s\n", remotePath)
		logger.Step("removing file %s", remotePath)
		if err := client.SFTP.Remove(remotePath); err != nil {
			return nil, fmt.Errorf("removing %s: %w", remotePath, err)
		}
		return []string{remotePath}, nil
	}

	sorted := make([]remoteEntry, len(children))
	copy(sorted, children)
	sort.Slice(sorted, func(i, j int) bool {
		di := strings.Count(sorted[i].relPath, "/")
		dj := strings.Count(sorted[j].relPath, "/")
		if di != dj {
			return di > dj // deeper paths first
		}
		return sorted[i].relPath > sorted[j].relPath
	})

	total := len(sorted) + 1 // +1 for the target directory itself, removed last
	deleted := make([]string, 0, total)
	for i, c := range sorted {
		full := path.Join(remotePath, c.relPath)
		fmt.Fprintf(out, "Deleting %d/%d: %s\n", i+1, total, full)
		var err error
		if c.isDir {
			logger.Step("removing directory %s", full)
			err = client.SFTP.RemoveDirectory(full)
		} else {
			logger.Step("removing file %s", full)
			err = client.SFTP.Remove(full)
		}
		if err != nil {
			logger.Error("failed removing %s: %v", full, err)
			return deleted, fmt.Errorf("removing %s: %w", full, err)
		}
		deleted = append(deleted, full)
	}

	fmt.Fprintf(out, "Deleting %d/%d: %s\n", total, total, remotePath)
	logger.Step("removing directory %s", remotePath)
	if err := client.SFTP.RemoveDirectory(remotePath); err != nil {
		logger.Error("failed removing %s: %v", remotePath, err)
		return deleted, fmt.Errorf("removing %s: %w", remotePath, err)
	}
	deleted = append(deleted, remotePath)

	return deleted, nil
}

// maybeBackupBeforeDelete offers to download a copy of remotePath (a file,
// or the full contents of a directory) before it gets deleted. Returns the
// local backup directory path, or "" if the user declined.
func maybeBackupBeforeDelete(client *sftpclient.Client, reader *bufio.Reader, out io.Writer, logger *logging.Logger, repoRoot, remotePath string, isDir bool, children []remoteEntry) (string, error) {
	doBackup, err := promptYesNo(reader, out, "Back up before deleting?", true)
	if err != nil {
		return "", err
	}
	logger.Step("backup before delete requested for %s: %v", remotePath, doBackup)
	if !doBackup {
		return "", nil
	}

	backupDir, err := newBackupDir(repoRoot, remotePath)
	if err != nil {
		return "", err
	}
	logger.Step("backing up %s to %s before delete", remotePath, backupDir)

	if !isDir {
		dest := filepath.Join(backupDir, filepath.Base(remotePath))
		if err := downloadFile(client, logger, remotePath, dest); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "Backed up %s -> %s\n", remotePath, dest)
		return backupDir, nil
	}

	var files []remoteEntry
	for _, c := range children {
		if !c.isDir {
			files = append(files, c)
		}
	}

	downloaded := 0
	for i, c := range files {
		srcPath := path.Join(remotePath, c.relPath)
		destPath := filepath.Join(backupDir, filepath.FromSlash(c.relPath))
		fmt.Fprintf(out, "Backing up %d/%d: %s\n", i+1, len(files), srcPath)
		if err := downloadFile(client, logger, srcPath, destPath); err != nil {
			return "", err
		}
		downloaded++
	}
	fmt.Fprintf(out, "Backed up %d file(s) from %s -> %s\n", downloaded, remotePath, backupDir)
	return backupDir, nil
}

// promptConfirmDangerous requires an explicit "y" or "yes" answer to
// proceed; anything else (including a blank Enter) is treated as "no".
// Used only for the actual delete confirmation -- deliberately stricter
// than promptYesNo's default-able behavior, since there is no safe default
// for "should I permanently delete this."
func promptConfirmDangerous(reader *bufio.Reader, out io.Writer, question string) (bool, error) {
	fmt.Fprint(out, question+" [y/N]: ")
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// promptYesNo asks a yes/no question with a default used for a blank
// answer. Used for the (non-destructive) backup offer.
func promptYesNo(reader *bufio.Reader, out io.Writer, question string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	fmt.Fprint(out, question+suffix)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return defaultYes, nil
	}
}
