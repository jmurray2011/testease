package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/reap"
)

func TestReapFindsAbandonedSecretWorkspacesByOwnershipNotAge(t *testing.T) {
	t.Parallel()

	runsDir := t.TempDir()
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	writeRuntimeOwner(t, runsDir, "old-run", runtimeWorkspaceOwner{
		RunID: "old-run", User: user, Host: host, PID: 999999999,
	})
	writeRuntimeOwner(t, runsDir, "live-run", runtimeWorkspaceOwner{
		RunID: "live-run", User: user, Host: host, PID: os.Getpid(),
	})

	got, notices := assessRuntimeWorkspaces(runsDir, reap.Runner{User: user, Host: host})
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}
	if len(got) != 2 {
		t.Fatalf("workspaces = %+v", got)
	}
	fates := map[string]reap.Fate{}
	for _, item := range got {
		fates[item.Owner.RunID] = item.Fate
	}
	if fates["old-run"] != reap.Orphaned || fates["live-run"] != reap.Live {
		t.Errorf("fates = %v", fates)
	}
	if removable := removableRuntime(got); len(removable) != 1 || removable[0].Owner.RunID != "old-run" {
		t.Errorf("removable = %+v", removable)
	}
}

func TestRemovingARuntimeWorkspacePrunesItsEmptyParents(t *testing.T) {
	t.Parallel()

	runsDir := t.TempDir()
	workspace := filepath.Join(runsDir, runtimeWorkDir, "old-run", "carry-123")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "owner.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeRuntimeWorkspace(runsDir, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runsDir, runtimeWorkDir)); !os.IsNotExist(err) {
		t.Fatalf("empty runtime root remains after reap: %v", err)
	}
}

func writeRuntimeOwner(t *testing.T, runsDir, name string, owner runtimeWorkspaceOwner) {
	t.Helper()
	dir := filepath.Join(runsDir, runtimeWorkDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
