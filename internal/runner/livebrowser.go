package runner

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmurray2011/testease/internal/livebrowser"
)

// liveHookFile is the node hook a drive step loads when live browser is on.
// It saves what the suite's Playwright page shows about once a second; see
// package livebrowser for who reads the frames.
const liveHookFile = "hook.cjs"

//go:embed livebrowser.cjs
var liveHook []byte

// prepareLive puts the hook and the owner file in a drive step's workspace and
// sets the environment that loads it. It returns an error only for a
// workspace it could not write to; the step then runs without frames.
func prepareLive(workspace, recordDir string, seq int, label string, env map[string]string) error {
	dir := filepath.Join(workspace, livebrowser.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("make the live browser directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, liveHookFile), liveHook, 0o644); err != nil {
		return fmt.Errorf("write the live browser hook: %w", err)
	}
	abs, err := filepath.Abs(recordDir)
	if err != nil {
		return fmt.Errorf("locate the record for the live browser: %w", err)
	}
	owner, err := json.Marshal(livebrowser.Owner{Record: abs, Step: seq, Label: label})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, livebrowser.OwnerFile), owner, 0o644); err != nil {
		return fmt.Errorf("write the live browser owner: %w", err)
	}

	inside := ContainerWorkspace + "/" + livebrowser.Dir
	env[livebrowser.DirEnv] = inside
	// Added to whatever the step asked for, never in place of it.
	require := "--require " + inside + "/" + liveHookFile
	env["NODE_OPTIONS"] = strings.TrimSpace(env["NODE_OPTIONS"] + " " + require)
	return nil
}
