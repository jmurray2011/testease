package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/doctor"
	"github.com/jmurray2011/testease/internal/judge"
)

// A Windows checkout with core.autocrlf converts a probe's line endings, and
// a Linux target then reads each carriage return as part of the command:
// measured, `set -e` fails as "set: -" and the probe dies before taking a
// reading. That is a property of the files, so doctor says so before a run.
func TestDoctorWarnsAboutAShellProbeWithWindowsLineEndings(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())
	probe := filepath.Join(root, name, "probes", "install.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/bash\r\nset -e\r\ntrue\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name, Docker: fakeDocker{version: "29.0"},
	})
	c := check(t, r, "line-endings")
	if c.Status != doctor.Warn {
		t.Fatalf("line-endings = %s (%s), want a warning", c.Status, c.Found)
	}
	if !strings.Contains(c.Found, "probes/install.sh") {
		t.Errorf("found = %q, want it to name the probe", c.Found)
	}
	if !strings.Contains(c.Remedy, ".gitattributes") || !strings.Contains(c.Remedy, "eol=lf") {
		t.Errorf("remedy = %q, want the .gitattributes fix", c.Remedy)
	}
}

func TestDoctorPassesShellProbesWithUnixLineEndings(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name, Docker: fakeDocker{version: "29.0"},
	})
	if c := check(t, r, "line-endings"); c.Status != doctor.OK {
		t.Errorf("line-endings = %s (%s), want ok", c.Status, c.Found)
	}
}

// PowerShell reads CRLF as a line ending, so a Windows probe that has them is
// not a problem and is not reported as one.
func TestDoctorLeavesAPowerShellProbesLineEndingsAlone(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())
	dir := filepath.Join(root, name)
	if err := os.WriteFile(filepath.Join(dir, "probes", "install.ps1"), []byte("$ErrorActionPreference = 'Stop'\r\nexit 0\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scenario.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), "probes/install.sh", "probes/install.ps1", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name, Docker: fakeDocker{version: "29.0"},
	})
	if c := check(t, r, "line-endings"); c.Status != doctor.OK {
		t.Errorf("line-endings = %s (%s), want ok for a PowerShell probe", c.Status, c.Found)
	}
}
