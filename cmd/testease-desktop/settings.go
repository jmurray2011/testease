package main

import (
	"path/filepath"
	"strings"
)

// prefRunsDir is the one thing this app remembers. A path changes where
// records are and never what is measured, so it is safe to keep; the values
// that change a measurement are never remembered anywhere.
const prefRunsDir = "runs-dir"

// prefScenarioRoot is the scenario root last chosen here: a path, like the
// runs directory.
const prefScenarioRoot = "scenario-root"

// resolveRunsDir follows the house precedence - flag, then environment, then a
// default - with the folder last chosen here standing in for the default. The
// CLI's own default is relative to the working directory, which means nothing
// to an app started from a menu. It also says where the answer came from, so
// the app can show it.
func resolveRunsDir(flag, env, remembered string) (dir, from string) {
	return resolvePath(flag, "--runs-dir", env, "TESTEASE_RUNS_DIR", remembered)
}

// resolvePath is the same precedence for any path this app remembers.
func resolvePath(flag, flagName, env, envName, remembered string) (dir, from string) {
	switch {
	case flag != "":
		return flag, flagName
	case env != "":
		return env, envName
	case remembered != "":
		return remembered, "last folder chosen here"
	default:
		return "", ""
	}
}

// sidebarPath is a runs directory as the narrow sidebar shows it: the folder's
// own name, whole, and on a second line the folder it is in, shortened to its
// last element, and that cut in the middle when it is too long for the line.
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
	return name, "in …" + sep + elideMiddle(filepath.Base(parent), sidebarParentMax)
}

// sidebarParentMax is as many characters of the parent folder's name as fit
// on the sidebar's line beside "in …/".
const sidebarParentMax = 18

// elideMiddle shortens s to at most n characters by cutting out its middle,
// so both its start and its end - often a date or a number - stay readable.
func elideMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n || n < 3 {
		return s
	}
	keep := n - 1
	head := (keep + 1) / 2
	return string(r[:head]) + "…" + string(r[len(r)-(keep-head):])
}
