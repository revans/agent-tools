// Package logging writes one timestamped log file per wharf run to
// scripts/wharf/log/, so a run's full step-by-step history (and full error
// detail on failure) can be read back afterward without re-running
// anything.
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Logger wraps a per-run log file plus a standard *log.Logger writer.
type Logger struct {
	file *os.File
	std  *log.Logger
	Path string
}

// New creates scripts/wharf/log/ if needed and opens a new timestamped log
// file for the given command name (e.g. "explore", "copy", "sync").
//
// File naming convention: wharf-<command>-<YYYYMMDD-HHMMSS>.log, one file
// per run. To find the most recent run's log, sort the log directory by
// filename (or modification time) and take the last entry.
func New(logDir, command string) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log directory %s: %w", logDir, err)
	}

	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("wharf-%s-%s.log", command, ts)
	path := filepath.Join(logDir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("creating log file %s: %w", path, err)
	}

	std := log.New(f, "", log.Ldate|log.Ltime)
	l := &Logger{file: f, std: std, Path: path}
	l.std.Printf("=== wharf %s started ===", command)
	return l, nil
}

// Step records a normal step of the run (a connection attempt, a file
// walked, a file uploaded, etc).
func (l *Logger) Step(format string, args ...any) {
	l.std.Printf(format, args...)
}

// Error records full error detail for a failure, so an LLM or human
// reading the log afterward understands exactly what went wrong without
// needing to reproduce it.
func (l *Logger) Error(format string, args ...any) {
	l.std.Printf("ERROR: "+format, args...)
}

// Writer exposes the underlying log file as an io.Writer, for callers
// (like an SSH client) that want to attach their own line-oriented debug
// output directly.
func (l *Logger) Writer() io.Writer {
	return l.file
}

// Close records the run's completion and closes the log file.
func (l *Logger) Close(finalErr error) error {
	if finalErr != nil {
		l.std.Printf("=== wharf run failed: %v ===", finalErr)
	} else {
		l.std.Printf("=== wharf run completed successfully ===")
	}
	return l.file.Close()
}
