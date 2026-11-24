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

// sidebarPath is a runs directory as the narrow sidebar shows it: the folder's
// own name, whole, and on a second line the folder it is in, shortened to its
// last element.
func sidebarPath(p string) (name, in string) {
	clean := filepath.Clean(p)
	name = filepath.Base(clean)
	parent := filepath.Dir(clean)
	if parent == "." || parent == clean {
		return name, ""
	}
	sep := string(filepath.Separator)
	trimmed := strings.TrimPrefix(parent, filepath.VolumeName(parent))
	if strings.Count(strings.Trim(trimmed, sep), sep) == 0 {
		return name, "in " + parent
	}
	return name, "in …" + sep + filepath.Base(parent)
}
