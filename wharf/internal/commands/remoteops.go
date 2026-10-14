package commands

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"wharf/internal/config"
	"wharf/internal/logging"
	"wharf/internal/pathmap"
)

// resolveRemote maps a user-supplied path (relative to the remote base
// path, the way WHARF.md lists paths) to an absolute remote path, and
// refuses anything that would land outside the base path. allowRoot
// permits the base path itself.
func resolveRemote(cfg *config.Config, arg string, allowRoot bool) (string, error) {
	root := normalizeRemoteRoot(cfg.RemoteBasePath)
	p := pathmap.RemotePath(cfg.RemoteBasePath, arg)
	if !remoteWithin(root, p) {
		return "", fmt.Errorf("%q resolves to %s, which is outside the remote base path %s", arg, p, root)
	}
	if p == root && !allowRoot {
		return "", fmt.Errorf("%q is the remote base path itself; pass a path inside it", arg)
	}
	return p, nil
}

// --- pull ---

type pullItem struct {
	remote string
	local  string
	size   int64
	mod    time.Time
}

// Pull downloads a remote file or directory. With no localArg the files
// mirror the repo layout (remote <base>/a/b.html -> <repo>/a/b.html), so
// pulled files drop straight into the site. Existing local files are never
// overwritten unless force is set. Downloaded files get the remote
// modification time, so a following sync sees them as unchanged.
func Pull(cfg *config.Config, repoRoot, remoteArg, localArg string, dryRun, force bool, logger *logging.Logger, out io.Writer) error {
	root := normalizeRemoteRoot(cfg.RemoteBasePath)
	remote, err := resolveRemote(cfg, remoteArg, true)
	if err != nil {
		return logged(logger, err)
	}

	var localBase string
	switch {
	case localArg != "":
		if localBase, err = filepath.Abs(localArg); err != nil {
			return logged(logger, err)
		}
	case remote == root:
		localBase = repoRoot
	default:
		localBase = filepath.Join(repoRoot, filepath.FromSlash(strings.TrimPrefix(remote, root+"/")))
	}

	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	info, err := client.SFTP.Stat(remote)
	if err != nil {
		return logged(logger, fmt.Errorf("remote path %s does not exist or is not accessible: %w", remote, err))
	}

	var items []pullItem
	if info.IsDir() {
		entries, err := walkRemoteTree(client, remote, logger)
		if err != nil {
			return logged(logger, err)
		}
		for _, e := range entries {
			if e.isDir || e.relPath == "" {
				continue
			}
			items = append(items, pullItem{
				remote: path.Join(remote, e.relPath),
				local:  filepath.Join(localBase, filepath.FromSlash(e.relPath)),
				size:   e.size, mod: e.modTime,
			})
		}
	} else {
		dst := localBase
		if st, err := os.Stat(localBase); err == nil && st.IsDir() {
			dst = filepath.Join(localBase, path.Base(remote))
		}
		items = append(items, pullItem{remote: remote, local: dst, size: info.Size(), mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].remote < items[j].remote })

	var todo []pullItem
	skipped := 0
	for _, it := range items {
		if _, err := os.Stat(it.local); err == nil && !force {
			skipped++
			fmt.Fprintf(out, "  skip (exists) %s\n", it.local)
			logger.Step("skip existing %s", it.local)
			continue
		}
		todo = append(todo, it)
		fmt.Fprintf(out, "  pull %s -> %s (%d bytes)\n", it.remote, it.local, it.size)
	}
	logger.Step("pull plan: %d to download, %d skipped (force=%v)", len(todo), skipped, force)

	if dryRun {
		fmt.Fprintf(out, "\n[dry-run] %d file(s) would be downloaded, %d skipped. Nothing was changed.\n", len(todo), skipped)
		return nil
	}
	for i, it := range todo {
		fmt.Fprintf(out, "Downloading %d/%d: %s\n", i+1, len(todo), it.remote)
		if err := downloadFile(client, logger, it.remote, it.local); err != nil {
			return logged(logger, err)
		}
		if !it.mod.IsZero() {
			if err := os.Chtimes(it.local, it.mod, it.mod); err != nil {
				logger.Error("downloaded %s but could not set its mtime: %v", it.local, err)
			}
		}
	}
	fmt.Fprintf(out, "Downloaded %d file(s)", len(todo))
	if skipped > 0 {
		fmt.Fprintf(out, ", skipped %d that already exist locally (use --force to overwrite)", skipped)
	}
	fmt.Fprintln(out, ".")
	return nil
}

// --- cat ---

// Cat writes a remote file's contents to out. Nothing else is written to
// out, so the result can be piped or redirected.
func Cat(cfg *config.Config, remoteArg string, logger *logging.Logger, out io.Writer) error {
	remote, err := resolveRemote(cfg, remoteArg, false)
	if err != nil {
		return logged(logger, err)
	}
	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	info, err := client.SFTP.Stat(remote)
	if err != nil {
		return logged(logger, fmt.Errorf("remote path %s does not exist or is not accessible: %w", remote, err))
	}
	if info.IsDir() {
		return logged(logger, fmt.Errorf("%s is a directory; cat reads files (use `wharf explore` to list)", remote))
	}
	f, err := client.SFTP.Open(remote)
	if err != nil {
		return logged(logger, fmt.Errorf("opening %s: %w", remote, err))
	}
	defer f.Close()
	n, err := io.Copy(out, f)
	if err != nil {
		return logged(logger, fmt.Errorf("reading %s: %w", remote, err))
	}
	logger.Step("cat %s (%d bytes)", remote, n)
	return nil
}

// --- mkdir ---

// Mkdir creates a remote directory and any missing parents. It succeeds
// quietly if the directory already exists and fails if a file is in the way.
func Mkdir(cfg *config.Config, remoteArg string, dryRun bool, logger *logging.Logger, out io.Writer) error {
	remote, err := resolveRemote(cfg, remoteArg, false)
	if err != nil {
		return logged(logger, err)
	}
	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	if info, err := client.SFTP.Stat(remote); err == nil {
		if info.IsDir() {
			fmt.Fprintf(out, "%s already exists.\n", remote)
			return nil
		}
		return logged(logger, fmt.Errorf("%s exists and is a file", remote))
	}
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would create directory %s. Nothing was changed.\n", remote)
		return nil
	}
	if err := client.SFTP.MkdirAll(remote); err != nil {
		return logged(logger, fmt.Errorf("creating %s: %w", remote, err))
	}
	logger.Step("created directory %s", remote)
	fmt.Fprintf(out, "Created %s\n", remote)
	return nil
}

// --- chmod ---

var modePattern = regexp.MustCompile(`^0?[0-7]{3}$`)

// parseMode parses an octal permission such as "644" or "0755". Setuid,
// setgid, and sticky bits are rejected: on a static site they are never
// wanted, and os.FileMode does not represent them as raw octal bits.
func parseMode(s string) (os.FileMode, error) {
	if !modePattern.MatchString(s) {
		return 0, fmt.Errorf("invalid mode %q: use three octal digits such as 644 or 755", s)
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, err
	}
	return os.FileMode(v), nil
}

// dirMode derives a directory's mode from a file mode by granting execute
// (traverse) wherever read is granted: 644 -> 755, 640 -> 750, 600 -> 700.
func dirMode(m os.FileMode) os.FileMode {
	return m | ((m & 0o444) >> 2)
}

// Chmod changes permissions on a remote file, or with recursive on a whole
// directory tree. Recursively, files get mode and directories get
// dirMode(mode), so 644 produces 644 files in 755 directories.
func Chmod(cfg *config.Config, modeArg, remoteArg string, recursive, dryRun bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	mode, err := parseMode(modeArg)
	if err != nil {
		return logged(logger, err)
	}
	remote, err := resolveRemote(cfg, remoteArg, recursive)
	if err != nil {
		return logged(logger, err)
	}
	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	info, err := client.SFTP.Stat(remote)
	if err != nil {
		return logged(logger, fmt.Errorf("remote path %s does not exist or is not accessible: %w", remote, err))
	}

	type target struct {
		path string
		mode os.FileMode
	}
	var targets []target
	if !recursive || !info.IsDir() {
		m := mode
		if info.IsDir() {
			m = dirMode(mode)
		}
		targets = append(targets, target{remote, m})
	} else {
		entries, err := walkRemoteTree(client, remote, logger)
		if err != nil {
			return logged(logger, err)
		}
		for _, e := range entries {
			m := mode
			if e.isDir {
				m = dirMode(mode)
			}
			targets = append(targets, target{path.Join(remote, e.relPath), m})
		}
	}

	fmt.Fprintf(out, "Will set permissions on %d path(s) under %s (files %04o, directories %04o).\n", len(targets), remote, mode, dirMode(mode))
	if dryRun {
		fmt.Fprintln(out, "[dry-run] Nothing was changed.")
		return nil
	}
	if recursive {
		ok, err := promptConfirmDangerous(bufio.NewReader(in), out, "Apply?")
		if err != nil {
			return logged(logger, err)
		}
		if !ok {
			fmt.Fprintln(out, "Aborted. Nothing was changed.")
			return nil
		}
	}
	for _, t := range targets {
		if err := client.SFTP.Chmod(t.path, t.mode); err != nil {
			return logged(logger, fmt.Errorf("chmod %04o %s: %w", t.mode, t.path, err))
		}
		logger.Step("chmod %04o %s", t.mode, t.path)
	}
	fmt.Fprintf(out, "Changed permissions on %d path(s).\n", len(targets))
	return nil
}

// --- prune (report only) ---

// findOrphans returns the remote files (paths relative to the base path)
// that have no counterpart among the local site files. Top-level entries
// named "archive" or starting with "." are left out and counted in skipped
// by name, because they are intentionally kept (archives, host config).
func findOrphans(remoteFiles []string, local map[string]bool) (orphans []string, skipped map[string]int) {
	skipped = map[string]int{}
	for _, rel := range remoteFiles {
		top := rel
		if i := strings.Index(rel, "/"); i >= 0 {
			top = rel[:i]
		}
		if top == archiveRootName || strings.HasPrefix(top, ".") {
			skipped[top]++
			continue
		}
		if !local[rel] {
			orphans = append(orphans, rel)
		}
	}
	sort.Strings(orphans)
	return orphans, skipped
}

// Prune lists remote files that are not part of the local site. It never
// deletes anything: it prints the `wharf delete` commands to run if you
// agree with the list.
func Prune(cfg *config.Config, repoRoot string, logger *logging.Logger, out io.Writer) error {
	candidates, err := localCandidates(repoRoot, logger)
	if err != nil {
		return err
	}
	local := map[string]bool{}
	for _, c := range candidates {
		local[c.relPath] = true
	}

	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	root := normalizeRemoteRoot(cfg.RemoteBasePath)
	entries, err := walkRemoteTree(client, root, logger)
	if err != nil {
		return logged(logger, err)
	}
	var remoteFiles []string
	for _, e := range entries {
		if !e.isDir && e.relPath != "" {
			remoteFiles = append(remoteFiles, e.relPath)
		}
	}
	orphans, skipped := findOrphans(remoteFiles, local)

	for name, n := range skipped {
		fmt.Fprintf(out, "Skipped %s (%d file(s)): intentionally kept\n", name, n)
	}
	if len(orphans) == 0 {
		fmt.Fprintln(out, "No orphaned remote files. The server matches the local site.")
		return nil
	}
	fmt.Fprintf(out, "\n%d remote file(s) have no local counterpart:\n", len(orphans))
	for _, o := range orphans {
		why := "not present locally"
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(o))); err == nil {
			why = "present locally but excluded by .wharfignore"
		}
		fmt.Fprintf(out, "  %s  (%s)\n", o, why)
		logger.Step("orphan: %s (%s)", o, why)
	}
	fmt.Fprintln(out, "\nNothing was deleted. To remove one, review it and run:")
	for _, o := range orphans {
		fmt.Fprintf(out, "  wharf delete %s\n", o)
	}
	return nil
}

// --- restore ---

// matchBackupDirs returns, oldest first, the backup directory names of the
// form "<timestamp>-<flattened>" for exactly this flattened remote path.
func matchBackupDirs(names []string, flattened string) []string {
	tsLen := len(backupTimestampLayout)
	var out []string
	for _, n := range names {
		if len(n) > tsLen+1 && n[tsLen] == '-' && n[tsLen+1:] == flattened {
			if _, err := time.Parse(backupTimestampLayout, n[:tsLen]); err == nil {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Restore re-uploads a file from wharf's local pre-overwrite backups (made
// by copy/sync/deploy). By default it uses the newest backup; from selects
// another by directory name or timestamp prefix. With list it only prints
// the available backups and needs no connection. The file being replaced is
// itself backed up first, so a restore can be undone.
func Restore(cfg *config.Config, repoRoot, remoteArg, from string, list, dryRun bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	remote, err := resolveRemote(cfg, remoteArg, false)
	if err != nil {
		return logged(logger, err)
	}
	backupsRoot := filepath.Join(repoRoot, "scripts", "wharf", "backups")
	dirs, err := os.ReadDir(backupsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return logged(logger, err)
	}
	var names []string
	for _, d := range dirs {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	matches := matchBackupDirs(names, flattenRemotePath(remote))
	if len(matches) == 0 {
		return logged(logger, fmt.Errorf("no backups found for %s in %s (backups are made when copy/sync overwrite an existing file)", remote, backupsRoot))
	}

	if list {
		fmt.Fprintf(out, "Backups of %s (newest first):\n", remote)
		for i := len(matches) - 1; i >= 0; i-- {
			f := filepath.Join(backupsRoot, matches[i], path.Base(remote))
			size := int64(-1)
			if st, err := os.Stat(f); err == nil {
				size = st.Size()
			}
			fmt.Fprintf(out, "  %s  (%d bytes)\n", matches[i], size)
		}
		return nil
	}

	chosen := matches[len(matches)-1]
	if from != "" {
		chosen = ""
		for _, m := range matches {
			if m == from || strings.HasPrefix(m, from) {
				chosen = m
			}
		}
		if chosen == "" {
			return logged(logger, fmt.Errorf("no backup matching %q for %s (see `wharf restore --list %s`)", from, remote, remoteArg))
		}
	}
	file := filepath.Join(backupsRoot, chosen, path.Base(remote))
	st, err := os.Stat(file)
	if err != nil {
		return logged(logger, fmt.Errorf("backup %s is not a restorable file: %w", file, err))
	}
	fmt.Fprintf(out, "Restore %s\n  from backup %s (%d bytes)\n  The file currently on the server will be backed up first.\n", remote, chosen, st.Size())
	if dryRun {
		fmt.Fprintln(out, "\n[dry-run] Nothing was changed.")
		return nil
	}
	ok, err := promptConfirmDangerous(bufio.NewReader(in), out, "\nRestore it?")
	if err != nil {
		return logged(logger, err)
	}
	if !ok {
		fmt.Fprintln(out, "Aborted. Nothing was restored.")
		return nil
	}
	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := uploadFile(client, logger, repoRoot, file, remote, false); err != nil {
		return logged(logger, err)
	}
	return nil
}

// --- diff ---

// isBinary reports whether data looks like a binary file (a NUL byte in
// the first 8 KiB).
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// Diff shows how the server's copy of a file differs from the local one,
// as a unified diff (remote is "-", local is "+": what a deploy would
// change). It shells out to the system `diff` for text files.
func Diff(cfg *config.Config, repoRoot, localArg string, logger *logging.Logger, out io.Writer) error {
	rel, err := pathmap.RepoRelative(repoRoot, localArg)
	if err != nil {
		return logged(logger, err)
	}
	remote := pathmap.RemotePath(cfg.RemoteBasePath, rel)
	localData, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		return logged(logger, fmt.Errorf("reading local file: %w", err))
	}

	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	f, err := client.SFTP.Open(remote)
	if err != nil {
		fmt.Fprintf(out, "%s does not exist on the server; a deploy would upload it as new (%d bytes).\n", rel, len(localData))
		return nil
	}
	defer f.Close()
	remoteData, err := io.ReadAll(f)
	if err != nil {
		return logged(logger, fmt.Errorf("reading %s: %w", remote, err))
	}

	if bytes.Equal(localData, remoteData) {
		fmt.Fprintf(out, "%s: no differences (%d bytes).\n", rel, len(localData))
		return nil
	}
	if isBinary(localData) || isBinary(remoteData) {
		fmt.Fprintf(out, "%s: binary files differ (remote %d bytes, local %d bytes).\n", rel, len(remoteData), len(localData))
		return nil
	}

	tmp, err := os.MkdirTemp("", "wharf-diff-")
	if err != nil {
		return logged(logger, err)
	}
	defer os.RemoveAll(tmp)
	rp, lp := filepath.Join(tmp, "remote"), filepath.Join(tmp, "local")
	if err := os.WriteFile(rp, remoteData, 0o600); err != nil {
		return logged(logger, err)
	}
	if err := os.WriteFile(lp, localData, 0o600); err != nil {
		return logged(logger, err)
	}
	cmd := exec.Command("diff", "-u", "--label", "remote/"+rel, "--label", "local/"+rel, rp, lp)
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil // exit 1 means "files differ", which is the point
		}
		return logged(logger, fmt.Errorf("running diff (is it installed?): %w; sizes: remote %d bytes, local %d bytes", err, len(remoteData), len(localData)))
	}
	return nil
}
