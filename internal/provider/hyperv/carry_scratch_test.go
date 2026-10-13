package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/target"
)

func TestCarryScratchIsRunOwnedAndRecoverableByReap(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "runtime", "carry-run")
	p := New(Options{ScratchRoot: root, RunID: "carry-run"})
	c := &cell{provider: p, spec: target.Spec{Host: "destination"}}
	if _, err := c.ReceiveCarry(context.Background(), bytes.NewBufferString("sensitive"), "/fixture.dump"); err != nil {
		t.Fatal(err)
	}

	staged := c.stagedMedia()
	if len(staged) != 1 || !strings.HasPrefix(staged[0].Local, root+string(filepath.Separator)) {
		t.Fatalf("staged scratch = %+v, want it under %s", staged, root)
	}
	scratch := filepath.Dir(staged[0].Local)
	if info, err := os.Stat(scratch); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("scratch mode = %v, %v; want 0700", info, err)
	}
	body, err := os.ReadFile(filepath.Join(scratch, "owner.json"))
	if err != nil {
		t.Fatalf("read owner: %v", err)
	}
	var owner struct {
		RunID string `json:"run_id"`
		Host  string `json:"host"`
		PID   int    `json:"pid"`
	}
	if err := json.Unmarshal(body, &owner); err != nil {
		t.Fatalf("owner json: %v", err)
	}
	if owner.RunID != "carry-run" || owner.Host == "" || owner.PID != os.Getpid() {
		t.Errorf("owner = %+v, want this run, host and process", owner)
	}

	if err := p.Release(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("scratch directory remains after release: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("empty run scratch root remains after release: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
		t.Errorf("empty runtime parent remains after release: %v", err)
	}
}
