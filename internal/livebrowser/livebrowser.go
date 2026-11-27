// Package livebrowser is the contract between a run that saves frames of a
// drive step's browser and a viewer that shows them.
//
// A drive step started with live browser on writes JPEG frames, about one a
// second, into a directory inside the step's workspace, beside an owner file
// that names the record and the step. A viewer finds the frames by that owner
// file. A browser on another machine - a drive on a remote daemon, or a
// probe that runs Playwright on its own host - keeps its newest frame under a
// fixed name, and the runner mirrors it here into a directory with the same
// owner file. The frames are working material, not evidence: both directories
// go when the step ends, and nothing copies them into the record.
package livebrowser

import (
	"bytes"
	"context"
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
	var owners []string
	for _, pattern := range []string{WorkspacePattern, MirrorPattern} {
		found, _ := filepath.Glob(filepath.Join(runsDir, pattern, Dir, OwnerFile))
		owners = append(owners, found...)
	}
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

// Latest is the file the hook keeps its newest frame in, under a name a
// runner on another machine can fetch without listing the directory; LatestAbout
// is that frame's page address.
const (
	Latest      = "latest.jpg"
	LatestAbout = "latest.json"

	// MirrorPattern matches a directory a runner keeps frames it fetched from
	// a browser that is not on this machine.
	MirrorPattern = ".testease-live-*"
)

// Every is how often frames are taken and fetched.
const Every = time.Second

// Fetch reads one file from wherever the browser runs.
type Fetch func(ctx context.Context, name string) ([]byte, error)

// Mirror copies the newest frame of a browser on another machine into local,
// a directory a viewer finds by its owner file, until ctx ends. A frame that
// cannot be fetched - none taken yet, the step still starting - is tried again
// on the next tick: watching never fails the step.
func Mirror(ctx context.Context, fetch Fetch, local string) {
	t := time.NewTicker(Every)
	defer t.Stop()
	var last []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		body, err := fetch(ctx, Latest)
		if err != nil || len(body) == 0 || bytes.Equal(body, last) {
			continue
		}
		about, _ := fetch(ctx, LatestAbout)
		if writeAtomic(filepath.Join(local, "frame-remote.jpg"), body) == nil {
			last = body
			if len(about) > 0 {
				_ = writeAtomic(filepath.Join(local, "frame-remote.json"), about)
			}
		}
	}
}

// Prepare makes a directory for mirrored frames under runsDir, with the
// owner file that tells a viewer whose they are. The caller removes it.
func Prepare(runsDir string, owner Owner) (root, frames string, err error) {
	root, err = os.MkdirTemp(runsDir, strings.TrimSuffix(MirrorPattern, "*"))
	if err != nil {
		return "", "", err
	}
	frames = filepath.Join(root, Dir)
	if err := os.MkdirAll(frames, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return "", "", err
	}
	body, err := json.Marshal(owner)
	if err == nil {
		err = os.WriteFile(filepath.Join(frames, OwnerFile), body, 0o644)
	}
	if err != nil {
		_ = os.RemoveAll(root)
		return "", "", err
	}
	return root, frames, nil
}

func writeAtomic(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
