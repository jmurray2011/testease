package main

import (
	"path/filepath"
	"strings"
)

// prefRunsDir is the one thing this app remembers. A path changes where
// records are and never what is measured, so it is safe to keep; the values
// that change a measurement are never remembered anywhere.
const prefRunsDir = "runs-dir"

// resolveRunsDir follows the house precedence - flag, then environment, then a
// default - with the folder last chosen here standing in for the default. The
// CLI's own default is relative to the working directory, which means nothing
// to an app started from a menu. It also says where the answer came from, so
// the app can show it.
func resolveRunsDir(flag, env, remembered string) (dir, from string) {
	switch {
	case flag != "":
		return flag, "--runs-dir"
	case env != "":
		return env, "TESTEASE_RUNS_DIR"
	case remembered != "":
		return remembered, "last folder chosen here"
	default:
		return "", ""
	}
}

// shortPath is a path as the narrow sidebar shows it: its last two elements,
// whole, after an ellipsis, rather than the whole path wrapped mid-word.
func shortPath(p string) string {
	clean := filepath.Clean(p)
	sep := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(clean, filepath.VolumeName(clean)), sep)
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	if len(kept) <= 2 {
		return p
	}
	return "…" + sep + filepath.Join(kept[len(kept)-2:]...)
}
