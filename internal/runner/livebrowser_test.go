package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/livebrowser"
)

func TestPrepareLiveAddsTheHookBesideTheStepsOwnNodeOptions(t *testing.T) {
	ws := t.TempDir()
	rec := filepath.Join(t.TempDir(), "run")
	env := map[string]string{"NODE_OPTIONS": "--max-old-space-size=4096"}
	if err := prepareLive(ws, rec, 3, "BROWSER: sign in", env); err != nil {
		t.Fatal(err)
	}
	want := "--max-old-space-size=4096 --require /workspace/.testease-live/hook.cjs"
	if env["NODE_OPTIONS"] != want {
		t.Errorf("NODE_OPTIONS = %q, want %q", env["NODE_OPTIONS"], want)
	}
	if env[livebrowser.DirEnv] != "/workspace/.testease-live" {
		t.Errorf("%s = %q", livebrowser.DirEnv, env[livebrowser.DirEnv])
	}
	if body, err := os.ReadFile(filepath.Join(ws, livebrowser.Dir, liveHookFile)); err != nil || len(body) == 0 {
		t.Errorf("hook not written: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(ws, livebrowser.Dir, livebrowser.OwnerFile))
	if err != nil {
		t.Fatal(err)
	}
	var o livebrowser.Owner
	if err := json.Unmarshal(body, &o); err != nil {
		t.Fatal(err)
	}
	if o.Record != rec || o.Step != 3 || o.Label != "BROWSER: sign in" {
		t.Errorf("owner = %+v", o)
	}
}
