package commands

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"wharf/internal/config"
	"wharf/internal/logging"
	"wharf/internal/pathmap"
	"wharf/internal/sftpclient"
)

// archiveRootName is the top-level directory archives are placed in. Each
// archive run gets its own timestamped subdirectory beneath it, so
// archiving twice never collides on names like index.html.
const archiveRootName = "archive"

// remoteWithin reports whether p is root itself or lies beneath it.
func remoteWithin(root, p string) bool {
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// Move renames (moves) a remote file or directory. Both arguments are
// relative to the configured remote base path, the same way WHARF.md lists
// paths. It never overwrites: if the destination already exists it refuses.
// Missing parent directories of the destination are created.
//
// A rename happens entirely on the server, so no file data is transferred
// and it is cheap and quickly reversible by swapping the arguments.
func Move(cfg *config.Config, fromArg, toArg string, dryRun bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	root := normalizeRemoteRoot(cfg.RemoteBasePath)
	from := pathmap.RemotePath(cfg.RemoteBasePath, fromArg)
	to := pathmap.RemotePath(cfg.RemoteBasePath, toArg)
	logger.Step("move resolved: %s -> %s (from arguments %q, %q)", from, to, fromArg, toArg)

	switch {
	case from == root || to == root:
		return logged(logger, fmt.Errorf("refusing to move the remote base path itself (%s); pass paths inside it", root))
	case !remoteWithin(root, from) || !remoteWithin(root, to):
		return logged(logger, fmt.Errorf("both paths must stay inside the remote base path %s (got %s -> %s)", root, from, to))
	case from == to:
		return logged(logger, fmt.Errorf("source and destination are the same path (%s)", from))
	case strings.HasPrefix(to, from+"/"):
		return logged(logger, fmt.Errorf("cannot move %s into itself (%s)", from, to))
	}

	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	info, err := client.SFTP.Stat(from)
	if err != nil {
		return logged(logger, fmt.Errorf("source %s does not exist or is not accessible: %w", from, err))
	}
	if _, err := client.SFTP.Stat(to); err == nil {
		return logged(logger, fmt.Errorf("destination %s already exists; wharf move never overwrites. Give the full new path, or delete/move the existing one first", to))
	}

	kind := "FILE"
	if info.IsDir() {
		kind = "DIRECTORY"
	}
	fmt.Fprintf(out, "About to move remote %s:\n  %s\n  -> %s\n", kind, from, to)

	if dryRun {
		fmt.Fprintln(out, "\n[dry-run] stopping before the move. Nothing was changed.")
		logger.Step("[dry-run] stopping before move; no changes made")
		return nil
	}

	ok, err := promptConfirmDangerous(bufio.NewReader(in), out, "\nMove it?")
	if err != nil {
		return logged(logger, err)
	}
	logger.Step("move confirmation: %v", ok)
	if !ok {
		fmt.Fprintln(out, "Aborted. Nothing was moved.")
		return nil
	}

	if err := client.SFTP.MkdirAll(path.Dir(to)); err != nil {
		return logged(logger, fmt.Errorf("creating destination directory %s: %w", path.Dir(to), err))
	}
	if err := client.SFTP.Rename(from, to); err != nil {
		return logged(logger, fmt.Errorf("renaming %s -> %s: %w", from, to, err))
	}
	logger.Step("moved %s -> %s", from, to)
	fmt.Fprintf(out, "Moved %s -> %s\n", from, to)
	return nil
}

type archiveEntry struct {
	name   string
	reason string // why it is staying put; empty for entries being moved
}

// planArchive splits the top-level names found in the remote base path
// into those that will be moved into the archive and those left in place.
// Left in place: the archive directory itself, hidden entries (names that
// start with "." -- typically host infrastructure such as .well-known,
// .htaccess, or provider diagnostics), and anything named in keep.
// Both result slices are sorted by name.
func planArchive(names, keep []string) (move []string, kept []archiveEntry) {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[strings.Trim(k, "/")] = true
	}
	for _, n := range names {
		switch {
		case n == archiveRootName:
			kept = append(kept, archiveEntry{n, "the archive directory itself"})
		case strings.HasPrefix(n, "."):
			kept = append(kept, archiveEntry{n, "hidden entry (host/config files)"})
		case keepSet[n]:
			kept = append(kept, archiveEntry{n, "named with --keep"})
		default:
			move = append(move, n)
		}
	}
	sort.Strings(move)
	sort.Slice(kept, func(i, j int) bool { return kept[i].name < kept[j].name })
	return move, kept
}

// Archive moves everything at the top level of the remote base path into
// archive/<timestamp>/, so a new version of the site can be uploaded to a
// clean root without losing the old one. Nothing is deleted or
// transferred: every move is a server-side rename.
//
// keep lists top-level names to leave in place (for example robots.txt).
func Archive(cfg *config.Config, keep []string, dryRun bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	root := normalizeRemoteRoot(cfg.RemoteBasePath)

	client, err := connect(cfg, logger)
	if err != nil {
		return err
	}
	defer client.Close()

	infos, err := client.SFTP.ReadDir(root)
	if err != nil {
		return logged(logger, fmt.Errorf("listing %s: %w", root, err))
	}
	isDir := map[string]bool{}
	var names []string
	for _, fi := range infos {
		names = append(names, fi.Name())
		isDir[fi.Name()] = fi.IsDir()
	}

	for _, k := range keep {
		found := false
		for _, n := range names {
			if n == strings.Trim(k, "/") {
				found = true
			}
		}
		if !found {
			fmt.Fprintf(out, "Note: --keep %q matches nothing at the top level of %s\n", k, root)
			logger.Step("--keep %q matched no top-level entry", k)
		}
	}

	move, kept := planArchive(names, keep)
	if len(move) == 0 {
		fmt.Fprintf(out, "Nothing to archive in %s.\n", root)
		logger.Step("nothing to archive")
		return nil
	}

	dest := path.Join(root, archiveRootName, time.Now().Format("20060102-150405"))
	fmt.Fprintf(out, "Archive destination: %s\n\nWill move %d item(s):\n", dest, len(move))
	for _, n := range move {
		suffix := ""
		if isDir[n] {
			suffix = "/"
		}
		fmt.Fprintf(out, "  %s%s\n", n, suffix)
	}
	if len(kept) > 0 {
		fmt.Fprintf(out, "\nLeaving in place:\n")
		for _, k := range kept {
			fmt.Fprintf(out, "  %s  (%s)\n", k.name, k.reason)
		}
	}
	logger.Step("archive plan: dest=%s move=%v kept=%v", dest, move, kept)

	if dryRun {
		fmt.Fprintln(out, "\n[dry-run] stopping before the move. Nothing was changed.")
		logger.Step("[dry-run] stopping before archive; no changes made")
		return nil
	}

	ok, err := promptConfirmDangerous(bufio.NewReader(in), out, fmt.Sprintf("\nMove %d item(s) into %s?", len(move), dest))
	if err != nil {
		return logged(logger, err)
	}
	logger.Step("archive confirmation: %v", ok)
	if !ok {
		fmt.Fprintln(out, "Aborted. Nothing was moved.")
		return nil
	}

	if err := client.SFTP.MkdirAll(dest); err != nil {
		return logged(logger, fmt.Errorf("creating archive directory %s: %w", dest, err))
	}
	for i, n := range move {
		src, dst := path.Join(root, n), path.Join(dest, n)
		fmt.Fprintf(out, "Moving %d/%d: %s\n", i+1, len(move), n)
		if err := client.SFTP.Rename(src, dst); err != nil {
			logger.Error("rename %s -> %s failed after %d of %d: %v", src, dst, i, len(move), err)
			return fmt.Errorf("moving %s: %w (%d of %d already moved into %s; see the log, and use `wharf move` to put them back if needed)", n, err, i, len(move), dest)
		}
		logger.Step("moved %s -> %s", src, dst)
	}

	rel := strings.TrimPrefix(dest, root+"/")
	fmt.Fprintf(out, "\nArchived %d item(s) into %s.\nThe site root is now clear for a fresh `wharf deploy`.\nTo restore one item: wharf move %s/<name> <name>\n", len(move), dest, rel)
	return nil
}

func connect(cfg *config.Config, logger *logging.Logger) (*sftpclient.Client, error) {
	logger.Step("connecting to %s:%d as %s", cfg.Host, cfg.Port, cfg.Username)
	client, err := sftpclient.Connect(cfg)
	if err != nil {
		logger.Error("connection failed: %v", err)
		return nil, fmt.Errorf("connecting: %w", err)
	}
	logger.Step("connected successfully")
	return client, nil
}

// logged records err in the run log and returns it unchanged.
func logged(logger *logging.Logger, err error) error {
	logger.Error("%v", err)
	return err
}
