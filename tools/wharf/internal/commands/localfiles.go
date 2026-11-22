package commands

import (
	"io/fs"
	"path/filepath"

	"wharf/internal/ignore"
	"wharf/internal/logging"
)

// localCandidates walks repoRoot and returns every file that is not
// excluded by .wharfignore: the set of files wharf considers part of the
// site. Shared by sync, prune and verify so they agree on what "the site"
// is.
func localCandidates(repoRoot string, logger *logging.Logger) ([]localCandidate, error) {
	ignorePath := filepath.Join(repoRoot, ".wharfignore")
	matcher, err := ignore.Load(ignorePath)
	if err != nil {
		logger.Error("failed to load %s: %v", ignorePath, err)
		return nil, err
	}
	logger.Step("loaded ignore patterns from %s", ignorePath)

	var candidates []localCandidate
	walkErr := filepath.WalkDir(repoRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == repoRoot {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, p)
		if relErr != nil {
			return relErr
		}
		relSlash := filepath.ToSlash(rel)
		if matcher.Match(relSlash, d.IsDir()) {
			logger.Step("ignoring %s (matched .wharfignore)", relSlash)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		candidates = append(candidates, localCandidate{relPath: relSlash, size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if walkErr != nil {
		logger.Error("failed walking local repo %s: %v", repoRoot, walkErr)
		return nil, walkErr
	}
	logger.Step("found %d local candidate file(s) after applying .wharfignore", len(candidates))
	return candidates, nil
}
