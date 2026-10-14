package commands

import (
	"fmt"
	"io"

	"wharf/internal/config"
	"wharf/internal/logging"
)

// Deploy runs the common-case "make the live site match my local repo"
// workflow as a single step: explore (refreshing WHARF.md) followed by
// sync. Explore has no destructive side effect, so dryRun only changes
// the sync half's behavior. noBackup is passed straight through to sync.
func Deploy(cfg *config.Config, repoRoot string, dryRun, noBackup bool, logger *logging.Logger, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "=== wharf deploy: step 1/2 - explore (refreshing WHARF.md) ===")
	logger.Step("deploy: running explore step")
	if err := Explore(cfg, repoRoot, logger); err != nil {
		logger.Error("deploy: explore step failed: %v", err)
		return fmt.Errorf("explore step failed: %w", err)
	}

	fmt.Fprintln(out, "\n=== wharf deploy: step 2/2 - sync ===")
	logger.Step("deploy: running sync step (dryRun=%v, noBackup=%v)", dryRun, noBackup)
	if err := Sync(cfg, repoRoot, dryRun, noBackup, logger, in, out); err != nil {
		logger.Error("deploy: sync step failed: %v", err)
		return fmt.Errorf("sync step failed: %w", err)
	}

	return nil
}
