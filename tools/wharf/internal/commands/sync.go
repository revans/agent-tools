package commands

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"wharf/internal/config"
	"wharf/internal/logging"
	"wharf/internal/pathmap"
	"wharf/internal/sftpclient"
)

// mtimeTolerance absorbs clock/timestamp-granularity differences between
// the local machine and the remote server when comparing modification
// times. Comparing raw mtimes across two different clocks is inherently
// heuristic -- see the README for the full tradeoff discussion. Size
// differences are the reliable signal; mtime is a best-effort add-on.
const mtimeTolerance = 2 * time.Second

type localCandidate struct {
	relPath string
	size    int64
	modTime time.Time
}

type remoteMeta struct {
	size    int64
	modTime time.Time
}

// Sync compares the local repo (relative to repoRoot, filtered through
// .wharfignore) against the remote SFTP contents and reports which local
// files are new or changed (by size and, best-effort, modification time).
// It then interactively prompts to upload all of them or a hand-picked
// subset.
//
// If dryRun is true, it still connects and computes the diff (that's the
// whole point of sync), but stops before the interactive prompt / upload
// step -- nothing is written anywhere.
//
// noBackup is passed straight through to uploadFile for each file that
// does get uploaded.
func Sync(cfg *config.Config, repoRoot string, dryRun, noBackup bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	candidates, err := localCandidates(repoRoot, logger)
	if err != nil {
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

	root := normalizeRemoteRoot(cfg.RemoteBasePath)
	logger.Step("walking remote tree from %s to diff against local files", root)
	entries, err := walkRemoteTree(client, root, logger)
	if err != nil {
		logger.Error("failed walking remote tree %s: %v", root, err)
		return fmt.Errorf("walking remote tree %s: %w", root, err)
	}
	remoteFiles := map[string]remoteMeta{}
	for _, e := range entries {
		if e.isDir {
			continue
		}
		remoteFiles[e.relPath] = remoteMeta{size: e.size, modTime: e.modTime}
	}
	logger.Step("remote tree contains %d file(s)", len(remoteFiles))

	var needsUpload []string
	for _, c := range candidates {
		remote, exists := remoteFiles[c.relPath]
		reason := ""
		switch {
		case !exists:
			reason = "missing remotely"
		case c.size != remote.size:
			reason = fmt.Sprintf("size differs (local %d bytes vs remote %d bytes)", c.size, remote.size)
		case !remote.modTime.IsZero() && absDuration(c.modTime.Sub(remote.modTime)) > mtimeTolerance:
			reason = fmt.Sprintf("modification time differs by more than %s (local %s vs remote %s)", mtimeTolerance, c.modTime.Format(time.RFC3339), remote.modTime.Format(time.RFC3339))
		default:
			continue
		}
		logger.Step("needs upload: %s (%s)", c.relPath, reason)
		needsUpload = append(needsUpload, c.relPath)
	}
	sort.Strings(needsUpload)
	logger.Step("%d local file(s) need uploading (new or changed)", len(needsUpload))

	if len(needsUpload) == 0 {
		fmt.Fprintln(out, "Everything is already up to date remotely (by size). Nothing to sync.")
		logger.Step("nothing to sync")
		return nil
	}

	fmt.Fprintf(out, "%d local file(s) need uploading (new or changed):\n", len(needsUpload))
	for i, m := range needsUpload {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, m)
	}

	if dryRun {
		logger.Step("[dry-run] would prompt to upload %d file(s); stopping before the interactive apply step", len(needsUpload))
		fmt.Fprintln(out, "\n[dry-run] stopping before the interactive upload step. Files listed above are what would be offered for upload. Nothing was changed.")
		return nil
	}

	selected, err := promptSelection(in, out, needsUpload)
	if err != nil {
		logger.Error("prompt failed: %v", err)
		return err
	}
	if len(selected) == 0 {
		fmt.Fprintln(out, "No files selected. Nothing uploaded.")
		logger.Step("user selected no files; nothing uploaded")
		return nil
	}

	logger.Step("uploading %d selected file(s)", len(selected))
	var uploadErrs []error
	for i, relPath := range selected {
		fmt.Fprintf(out, "Uploading %d/%d: %s\n", i+1, len(selected), relPath)
		localAbs := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		remotePath := pathmap.RemotePath(cfg.RemoteBasePath, relPath)
		if err := uploadFile(client, logger, repoRoot, localAbs, remotePath, noBackup); err != nil {
			uploadErrs = append(uploadErrs, err)
			fmt.Fprintf(out, "FAILED: %s (%v)\n", relPath, err)
			continue
		}
		fmt.Fprintf(out, "Uploaded: %s\n", relPath)
	}
	if len(uploadErrs) > 0 {
		return fmt.Errorf("%d of %d uploads failed", len(uploadErrs), len(selected))
	}
	return nil
}

// promptSelection asks the user whether to upload all missing/changed
// files, hand-pick a subset, or quit without uploading.
func promptSelection(in io.Reader, out io.Writer, needsUpload []string) ([]string, error) {
	reader := bufio.NewReader(in)
	fmt.Fprint(out, "\nUpload (a)ll, (s)elect individual files, or (q)uit? [a/s/q]: ")
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	choice := strings.ToLower(strings.TrimSpace(line))

	switch choice {
	case "a", "all":
		return needsUpload, nil
	case "q", "quit", "":
		return nil, nil
	case "s", "select":
		fmt.Fprint(out, "Enter comma-separated numbers to upload (e.g. 1,3,4): ")
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		parts := strings.Split(strings.TrimSpace(line), ",")
		var selected []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			idx, convErr := strconv.Atoi(part)
			if convErr != nil || idx < 1 || idx > len(needsUpload) {
				return nil, fmt.Errorf("invalid selection %q", part)
			}
			selected = append(selected, needsUpload[idx-1])
		}
		return selected, nil
	default:
		return nil, fmt.Errorf("unrecognized choice %q", choice)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
