package commands

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"wharf/internal/config"
	"wharf/internal/logging"
	"wharf/internal/pathmap"
	"wharf/internal/sftpclient"
)

// Copy uploads a single local file to the SFTP server. The destination is
// determined by mirroring localFileArg's path relative to repoRoot onto
// the same relative path under cfg.RemoteBasePath. Remote parent
// directories are created as needed.
//
// If dryRun is true, no connection is made at all: the local file is
// resolved and its would-be destination is logged/printed, and nothing is
// transferred.
//
// Unless noBackup is true, if a remote file already exists at the
// destination it is downloaded into scripts/wharf/backups/ before being
// overwritten.
func Copy(cfg *config.Config, repoRoot, localFileArg string, dryRun, noBackup bool, logger *logging.Logger) error {
	relPath, err := pathmap.RepoRelative(repoRoot, localFileArg)
	if err != nil {
		logger.Error("path resolution failed: %v", err)
		return err
	}

	localAbs, err := filepath.Abs(localFileArg)
	if err != nil {
		logger.Error("failed to resolve absolute path for %s: %v", localFileArg, err)
		return fmt.Errorf("resolving %s: %w", localFileArg, err)
	}

	info, err := os.Stat(localAbs)
	if err != nil {
		logger.Error("cannot stat local file %s: %v", localAbs, err)
		return fmt.Errorf("stat %s: %w", localAbs, err)
	}
	if info.IsDir() {
		err := fmt.Errorf("%s is a directory; wharf copy only uploads a single file", localFileArg)
		logger.Error("%v", err)
		return err
	}

	remotePath := pathmap.RemotePath(cfg.RemoteBasePath, relPath)
	logger.Step("resolved local file %s to repo-relative path %s (remote target: %s)", localFileArg, relPath, remotePath)

	if dryRun {
		logger.Step("[dry-run] would upload %s -> %s (%d bytes); no connection made", localAbs, remotePath, info.Size())
		fmt.Printf("[dry-run] would upload:\n  %s\n  -> %s (%d bytes)\n", localAbs, remotePath, info.Size())
		return nil
	}

	logger.Step("connecting to %s:%d as %s", cfg.Host, cfg.Port, cfg.Username)
	client, err := sftpclient.Connect(cfg)
	if err != nil {
		logger.Error("connection failed: %v", err)
		return fmt.Errorf("connecting: %w", err)
	}
	defer client.Close()
	logger.Step("connected successfully")

	return uploadFile(client, logger, repoRoot, localAbs, remotePath, noBackup)
}

// uploadFile is shared by copy and sync. Unless noBackup is true, it first
// backs up whatever currently exists at remotePath (if anything) into
// scripts/wharf/backups/, then ensures the remote parent directory exists
// and streams the local file's contents to the remote path.
func uploadFile(client *sftpclient.Client, logger *logging.Logger, repoRoot, localAbs, remotePath string, noBackup bool) error {
	if noBackup {
		logger.Step("--no-backup set; skipping pre-overwrite backup check for %s", remotePath)
	} else {
		backupPath, err := backupRemoteFile(client, logger, repoRoot, remotePath)
		if err != nil {
			logger.Error("pre-overwrite backup failed for %s: %v", remotePath, err)
			return fmt.Errorf("backing up %s before overwrite: %w", remotePath, err)
		}
		if backupPath != "" {
			fmt.Printf("Backed up existing remote file before overwrite: %s\n", backupPath)
		}
	}

	remoteDir := path.Dir(remotePath)
	logger.Step("ensuring remote directory exists: %s", remoteDir)
	if err := client.SFTP.MkdirAll(remoteDir); err != nil {
		logger.Error("failed to create remote directory %s: %v", remoteDir, err)
		return fmt.Errorf("creating remote directory %s: %w", remoteDir, err)
	}

	src, err := os.Open(localAbs)
	if err != nil {
		logger.Error("failed to open local file %s: %v", localAbs, err)
		return fmt.Errorf("opening local file %s: %w", localAbs, err)
	}
	defer src.Close()

	dst, err := client.SFTP.Create(remotePath)
	if err != nil {
		logger.Error("failed to create remote file %s: %v", remotePath, err)
		return fmt.Errorf("creating remote file %s: %w", remotePath, err)
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		logger.Error("failed uploading %s to %s: %v", localAbs, remotePath, err)
		return fmt.Errorf("uploading %s to %s: %w", localAbs, remotePath, err)
	}
	// Close before touching timestamps: some servers reset mtime when the
	// handle closes, and close is also where buffered write errors surface.
	if err := dst.Close(); err != nil {
		logger.Error("failed closing remote file %s: %v", remotePath, err)
		return fmt.Errorf("closing remote file %s: %w", remotePath, err)
	}

	// Stamp the remote file with the local file's modification time so the
	// next sync sees matching size and mtime and skips it. Without this the
	// server records the upload moment, which never equals the local mtime,
	// and sync would re-upload every file on every run. A server that
	// refuses this is not fatal: the file is uploaded and only sync's
	// skip-unchanged optimization is lost, so warn and carry on.
	if info, statErr := os.Stat(localAbs); statErr != nil {
		logger.Error("could not stat %s to preserve mtime: %v", localAbs, statErr)
	} else if err := client.SFTP.Chtimes(remotePath, info.ModTime(), info.ModTime()); err != nil {
		logger.Error("uploaded %s but could not set its mtime (sync may re-upload it next run): %v", remotePath, err)
		fmt.Printf("Warning: could not preserve modification time on %s: %v\n", remotePath, err)
	}

	logger.Step("uploaded %s -> %s (%d bytes)", localAbs, remotePath, n)
	fmt.Printf("Uploaded %s -> %s (%d bytes)\n", localAbs, remotePath, n)
	return nil
}
