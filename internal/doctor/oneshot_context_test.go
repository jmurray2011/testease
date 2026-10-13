package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/doctor"
)

func TestMissingOneShotImageRemedyNamesItsDockerContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "missing-one-shot")
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
schema: 1
name: missing-one-shot
question: can the requested one-shot image run?
hosts:
  destination: {provider: hyperv, image: windows-product:1}
steps:
  - {label: inspect, host: destination, probe: probes/inspect.ps1}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "inspect.ps1"), []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: "missing-one-shot",
		OneShotDocker:        fakeDocker{version: "29.7.1", images: map[string]bool{}},
		OneShotDockerContext: "winhost",
		Backends:             testBackends(),
	})
	got := check(t, r, "images")
	if got.Status != doctor.Fail {
		t.Fatalf("images = %s (%s), want fail", got.Status, got.Found)
	}
	if !strings.Contains(got.Remedy, "docker --context winhost pull windows-product:1") {
		t.Errorf("remedy = %q, want the daemon context in the pull command", got.Remedy)
	}
}
