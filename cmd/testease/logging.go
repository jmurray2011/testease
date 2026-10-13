package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"
)

// Diagnostics are a channel of their own, and these are the rules they keep.
//
//   - They go to stderr, always. Stdout is where a caller's data goes, and
//     `-o json` means something is parsing it.
//   - They are not the record. A record is never reconstructed from a log
//     line, and nothing here is evidence of anything.
//   - They are not notices either. "kept target X; remove it with ..." is an
//     instruction a person has to act on, so it is printed at any verbosity
//     rather than gated behind a level.
//   - Probe output never passes through here. It is evidence, kept verbatim
//     in the record, and a log is not allowed to paraphrase it.
func logger(cmd *cobra.Command) (*slog.Logger, error) {
	level, err := level(flagString(cmd, "log-level"))
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: level}

	// No colour, no progress rendering, no tty questions asked: a terminal and
	// a CI job get the same bytes.
	switch format := strings.ToLower(flagString(cmd, "log-format")); format {
	case "text":
		return slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), opts)), nil
	default:
		return nil, fmt.Errorf("no such log format %q; there is text and json", format)
	}
}

func level(name string) (slog.Level, error) {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("no such log level %q; there is debug, info, warn and error", name)
	}
}
