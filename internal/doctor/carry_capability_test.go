package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/doctor"
)

func TestDoctorRefusesAnIncompatibleCarrySourceBeforeTargetsStart(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	name := "bad-carry-source"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
schema: 1
name: bad-carry-source
question: can a one-shot source fetch bytes for another host?
hosts:
  source: {provider: hyperv, image: neutral-other:1}
  destination: {provider: docker, image: neutral-linux:1}
steps:
  - label: make something
    host: source
    probe: probes/read.ps1
  - label: carry it
    host: source
    carry: {from: /out.bin, to_host: destination, to: /in.bin}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "read.ps1"), []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Backends: []doctor.Backend{
			{Provider: "docker", Offers: []string{"session", "media"}},
			{Provider: "hyperv", Offers: []string{"oneshot", "carry"}},
		},
	})

	got := check(t, r, "capabilities")
	if got.Status != doctor.Fail {
		t.Fatalf("status = %q (%s), want fail", got.Status, got.Found)
	}
	if !strings.Contains(got.Found, "source") || !strings.Contains(got.Found, "session") {
		t.Errorf("found = %q, want source and session named", got.Found)
	}
}
