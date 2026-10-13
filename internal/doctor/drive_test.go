package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/doctor"
)

// A local drive step bind-mounts this machine's filesystem. A remote one
// transfers its question and evidence through the Docker context instead;
// doctor proves whichever path the runner will use before a box is booted.

const driveManifest = `
schema: 1
name: app-regression
question: does the suite still pass?
hosts:
  box:
    provider: docker
    image: neutral-base:1
    port: 8080
steps:
  - label: install
    host: box
    probe: probes/install.sh
    expect: {rc: 0}
  - label: drive the suite
    host: box
    probe: drive://suite:1
    expect: {rc: 0}
`

func driveScenario(t *testing.T) (root, name string) {
	t.Helper()

	root = t.TempDir()
	name = "app-regression"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(driveManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "install.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return root, name
}

func TestADriveScenarioAgainstARemoteDaemonPassesWhenEvidenceCanRoundTrip(t *testing.T) {
	t.Parallel()

	root, name := driveScenario(t)
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir:      t.TempDir(),
		ScenarioRoot: root,
		Scenario:     name,
		Docker: fakeDocker{
			version: "29.7.1", images: map[string]bool{"neutral-base:1": true, "suite:1": true},
			elsewhere: "dockersrv",
		},
		Backends: testBackends(),
	})

	c := check(t, r, "drive")
	if c.Status != doctor.OK {
		t.Errorf("drive check = %s (%s), want ok", c.Status, c.Found)
	}
	if !strings.Contains(c.Found, "dockersrv") {
		t.Errorf("found = %q, want the daemon named", c.Found)
	}
	if r.Worst() != doctor.OK {
		t.Errorf("report = %s, want ok", r.Worst())
	}
}

func TestARemoteDaemonThatCannotReturnEvidenceFailsBeforeABoxIsBooted(t *testing.T) {
	t.Parallel()

	root, name := driveScenario(t)
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir:      t.TempDir(),
		ScenarioRoot: root,
		Scenario:     name,
		Docker: fakeDocker{
			version: "29.7.1", images: map[string]bool{"neutral-base:1": true, "suite:1": true},
			elsewhere: "dockersrv", copyErr: os.ErrPermission,
		},
	})

	c := check(t, r, "drive")
	if c.Status != doctor.Fail {
		t.Errorf("drive check = %s (%s), want fail", c.Status, c.Found)
	}
	if !strings.Contains(c.Found, "dockersrv") || !strings.Contains(c.Found, "permission") {
		t.Errorf("drive refusal is not actionable: %+v", c)
	}
}

func TestADriveScenarioAgainstTheLocalDaemonPasses(t *testing.T) {
	t.Parallel()

	root, name := driveScenario(t)
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir:      t.TempDir(),
		ScenarioRoot: root,
		Scenario:     name,
		Docker:       fakeDocker{version: "29.7.1", images: map[string]bool{"neutral-base:1": true}},
	})

	if c := check(t, r, "drive"); c.Status != doctor.OK {
		t.Errorf("drive check = %s (%s), want ok", c.Status, c.Found)
	}
}

func TestALocalDaemonThatCannotSeeTheRunsDirectoryFailsTheDriveCheck(t *testing.T) {
	t.Parallel()

	root, name := driveScenario(t)
	runsDir := t.TempDir()
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir:      runsDir,
		ScenarioRoot: root,
		Scenario:     name,
		Docker: fakeDocker{
			version: "29.7.1", images: map[string]bool{"neutral-base:1": true, "suite:1": true},
			mountInvisible: true,
		},
	})

	c := check(t, r, "drive")
	if c.Status != doctor.Fail {
		t.Errorf("drive check = %s (%s), want fail", c.Status, c.Found)
	}
	if !strings.Contains(c.Found, runsDir) || !strings.Contains(c.Remedy, "runs-dir") {
		t.Errorf("drive refusal is not actionable: %+v", c)
	}
}

func TestDoctorChecksTheDriveImageAndMediaBeforeRunning(t *testing.T) {
	t.Parallel()

	root, name := driveScenario(t)
	dir := filepath.Join(root, name)
	suite := t.TempDir()
	body, err := os.ReadFile(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body),
		"probe: drive://suite:1",
		"probe: drive://suite:1\n    media: [\""+suite+":/suite:ro\"]", 1))
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Docker: fakeDocker{version: "29.7.1", images: map[string]bool{"neutral-base:1": true}},
	})
	if got := check(t, r, "images"); got.Status != doctor.Fail || !strings.Contains(got.Found, "suite:1") {
		t.Errorf("images = %s (%s), want missing drive image", got.Status, got.Found)
	}
	if got := check(t, r, "media"); got.Status != doctor.OK || !strings.Contains(got.Found, "1 mount") {
		t.Errorf("media = %s (%s), want the drive mount checked", got.Status, got.Found)
	}
}

// A scenario with no drive step has nothing to refuse, on any daemon.
func TestAScenarioThatDrivesNothingIsNotRefusedOnARemoteDaemon(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", "3")
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir:      t.TempDir(),
		ScenarioRoot: root,
		Scenario:     name,
		Docker:       fakeDocker{version: "29.7.1", images: map[string]bool{"neutral-base:1": true}, elsewhere: "dockersrv"},
	})

	if c := check(t, r, "drive"); c.Status != doctor.OK {
		t.Errorf("drive check = %s (%s), want ok", c.Status, c.Found)
	}
}

// Without a daemon to ask, the check has not passed - it was not made.
func TestWithoutADaemonTheDriveCheckIsUncheckedNotPassed(t *testing.T) {
	t.Parallel()

	root, name := driveScenario(t)
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir:      t.TempDir(),
		ScenarioRoot: root,
		Scenario:     name,
	})

	if c := check(t, r, "drive"); c.Status != doctor.Unchecked {
		t.Errorf("drive check = %s (%s), want unchecked", c.Status, c.Found)
	}
}

// A host that declares a port has already answered the one condition the
// endpoint capability carries, so there is nothing left for a dialled target
// to settle.
func TestAHostThatDeclaresAPortSatisfiesTheEndpointCapability(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	name := "serves"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := `
schema: 1
name: serves
question: does it answer?
requires: [endpoint]
hosts:
  box:
    provider: docker
    image: neutral-base:1
    port: 443
    scheme: https
steps:
  - label: read it
    host: box
    probe: probes/read.sh
    expect: {rc: 0}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "read.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Backends: testBackends(),
	})

	if got := check(t, r, "capabilities"); got.Status != doctor.OK {
		t.Errorf("status = %q (%s), want ok: the host declares a port", got.Status, got.Found)
	}
}
