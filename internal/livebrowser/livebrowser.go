// Package livebrowser is the contract between a run that saves frames of a
// drive step's browser and a viewer that shows them.
//
// A drive step started with live browser on writes JPEG frames, about one a
// second, into a directory inside the step's workspace, beside an owner file
// that names the record and the step. A viewer finds the frames by that owner
// file. The frames are working material, not evidence: the workspace goes
// when the step ends, and nothing copies them into the record.
package livebrowser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DirEnv names the directory the hook writes frames to, inside the
	// container.
	DirEnv = "TESTEASE_LIVE_DIR"

	// Dir is that directory's name inside the workspace.
	Dir = ".testease-live"

	// OwnerFile says whose frames these are.
	OwnerFile = "owner.json"

	// WorkspacePattern matches a drive step's workspace in the runs
	// directory.
	WorkspacePattern = ".testease-drive-*"
)

// Owner is what the owner file says.
type Owner struct {
	// Record is the absolute path of the record the step writes to: the
	// run's, or its cell's.
	Record string `json:"record"`
	Step   int    `json:"step"`
	Label  string `json:"label"`
}

// Frame is the newest picture a step's browser saved.
type Frame struct {
	Path string
	URL  string
	At   time.Time
}

// Find is the newest frame of step seq of the record at recordDir, looking in
// the drive workspaces under runsDir. ok is false when there is none: the run
// was started without live browser, the step is not a drive step, or the
// suite has not opened a page yet.
func Find(runsDir, recordDir string, seq int) (Frame, bool) {
	want, err := filepath.Abs(recordDir)
	if err != nil {
		return Frame{}, false
	}
	owners, _ := filepath.Glob(filepath.Join(runsDir, WorkspacePattern, Dir, OwnerFile))
	for _, path := range owners {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var o Owner
		if json.Unmarshal(body, &o) != nil || o.Step != seq || filepath.Clean(o.Record) != want {
			continue
		}
		return newest(filepath.Dir(path))
	}
	return Frame{}, false
}

// newest is the most recent frame in dir. Each node process of a suite writes
// its own, so a suite that runs workers side by side has several.
func newest(dir string) (Frame, bool) {
	frames, _ := filepath.Glob(filepath.Join(dir, "frame-*.jpg"))
	var best Frame
	for _, f := range frames {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if best.Path == "" || info.ModTime().After(best.At) {
			best = Frame{Path: f, At: info.ModTime()}
		}
	}
	if best.Path == "" {
		return Frame{}, false
	}
	if body, err := os.ReadFile(strings.TrimSuffix(best.Path, ".jpg") + ".json"); err == nil {
		var about struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(body, &about) == nil {
			best.URL = about.URL
		}
	}
	return best, true
}
