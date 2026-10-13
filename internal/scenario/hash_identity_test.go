package scenario_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/scenario"
)

func TestRuntimeIdentityIsSeparateFromTheCompleteScenarioTree(t *testing.T) {
	t.Parallel()

	dir := write(t, minimal)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("first citation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "suite.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := scenario.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if first.HashVersion != "runtime-v2" {
		t.Fatalf("hash version = %q, want runtime-v2", first.HashVersion)
	}
	if !strings.HasPrefix(first.TreeHash, "sha256:") {
		t.Fatalf("tree hash = %q", first.TreeHash)
	}
	for _, name := range []string{"README.md", "suite.sh", "probes/install.sh"} {
		if first.Files[name] == "" {
			t.Errorf("complete file inventory omits %s", name)
		}
	}
	for _, name := range []string{"README.md", "suite.sh"} {
		if _, runtimeInput := first.Probes[name]; runtimeInput {
			t.Errorf("documentation/test harness %s is recorded as an executed probe", name)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("new canonical citation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := scenario.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Hash != first.Hash {
		t.Errorf("README changed runtime identity from %s to %s", first.Hash, second.Hash)
	}
	if second.TreeHash == first.TreeHash {
		t.Error("README edit did not change the complete-tree identity")
	}
}
