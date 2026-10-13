package runner_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

// An artifact is a file worth keeping out of a step, and every kind of step can
// declare one. Two of the three have no session to fetch through - a one-shot
// cell stops existing when its command ends, and a drive step runs beside the
// target rather than on it - so each has to be told what to keep while it still
// can.

const oneShotArtifact = `
schema: 1
name: one-shot-artifact
question: does a cell keep what it was asked to?
hosts:
  box: {provider: fake, image: neutral-windows:1}
steps:
  - label: run the installer
    host: box
    probe: probes/one.sh
artifacts:
  - {step: run the installer, from: /testease/install.log, as: install.log, when: always}
`

func TestAOneShotCellKeepsItsArtifact(t *testing.T) {
	t.Parallel()

	// The container is the command: after it ends there is nothing to fetch
	// from, so what to take has to travel with what to run.
	tgt := &shotTarget{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "installed\n")
		if opt.Fetch == nil {
			return target.Result{}, nil
		}
		return target.Result{
			RC:      0,
			Fetched: map[string][]byte{opt.Fetch[0]: []byte("what the installer logged\n")},
		}, nil
	}}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, oneShotArtifact),
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": &shotProvider{target: tgt}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	body, readErr := os.ReadFile(filepath.Join(out.Dir, "artifacts", "install.log"))
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if !strings.Contains(string(body), "what the installer logged") {
		t.Errorf("artifact = %q", body)
	}
}

const driveArtifact = `
schema: 1
name: drive-artifact
question: does a runner-side step keep what it was asked to?
hosts:
  box: {provider: fake, image: neutral-base:1, port: 8080}
steps:
  - label: drive the app
    host: box
    probe: drive://suite:1
artifacts:
  - {step: drive the app, from: trace.txt, as: trace.txt, when: always}
`

func TestADriveStepKeepsItsArtifact(t *testing.T) {
	t.Parallel()

	// A drive step's files are in the workspace, which is on this machine.
	// Reaching for the target's session would fetch from the wrong box.
	driver := &fakeDriver{run: func(spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
		if err := os.WriteFile(filepath.Join(spec.Workspace, "trace.txt"), []byte("what the suite saw\n"), 0o644); err != nil {
			return target.Result{}, err
		}
		_, _ = io.WriteString(opt.Stdout, "suite finished\n")
		return target.Result{RC: 0}, nil
	}}

	out, _, err := drove(t, driveArtifact, driver, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body, readErr := os.ReadFile(filepath.Join(out.Dir, "artifacts", "trace.txt"))
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if !strings.Contains(string(body), "what the suite saw") {
		t.Errorf("artifact = %q", body)
	}
}

// A finding cites an artifact by the name the scenario declared, and there is
// no event naming any other one. A second attempt that kept its bytes under a
// numbered name left the cited name resolving to the first attempt's evidence
// with nothing saying so, so the second attempt refuses and the record carries
// the refusal.
func TestASecondAttemptWillNotKeepAnArtifactUnderANameNobodyCanCite(t *testing.T) {
	t.Parallel()

	kept := "what the first attempt logged\n"
	tgt := &shotTarget{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "installed\n")
		if opt.Fetch == nil {
			return target.Result{}, nil
		}
		return target.Result{RC: 0, Fetched: map[string][]byte{opt.Fetch[0]: []byte(kept)}}, nil
	}}

	runsDir := t.TempDir()
	attempt := func() *runner.Outcome {
		t.Helper()
		out, err := runner.Run(context.Background(), runner.Options{
			Plan:      plan(t, oneShotArtifact),
			RunsDir:   runsDir,
			RunID:     "one-shot-artifact-20260101T000000Z",
			Tool:      record.ToolRef{Version: "0.0.0-test"},
			Providers: map[string]target.Provider{"fake": &shotProvider{target: tgt}},
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return out
	}

	attempt()
	kept = "what the second attempt logged\n"
	out := attempt()

	body, err := os.ReadFile(filepath.Join(out.Dir, "artifacts", "install.log"))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.Contains(string(body), "first attempt") {
		t.Errorf("artifact = %q, want the attempt that kept it first left exactly as it was", body)
	}

	entries, err := os.ReadDir(filepath.Join(out.Dir, "artifacts"))
	if err != nil {
		t.Fatalf("read artifacts: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "install.log" {
			t.Errorf("kept %s, want nothing but the declared name: no event names any other", e.Name())
		}
	}

	rec, err := record.Read(out.Dir)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var said bool
	for _, e := range rec.Events {
		if e.Kind != record.KindRecordWarning {
			continue
		}
		w, err := record.Decode[record.Warning](e)
		if err != nil {
			t.Fatalf("decode warning: %v", err)
		}
		if strings.Contains(w.Message, "install.log") {
			said = true
		}
	}
	if !said {
		t.Error("the record says nothing about the artifact it did not keep")
	}
}

func TestAnArtifactThatCannotBeReachedIsSaidNotCrashed(t *testing.T) {
	t.Parallel()

	// Whatever else happens, a declared artifact that cannot be collected is a
	// warning. Recording never aborts a run, and it certainly never takes the
	// process down with it.
	tgt := &shotTarget{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "done\n")
		return target.Result{RC: 0}, nil // keeps nothing
	}}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, oneShotArtifact),
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": &shotProvider{target: tgt}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Measured != 1 {
		t.Errorf("measured = %d, want the step still measured", out.Measured)
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("read record: %v", readErr)
	}
	var warned bool
	for _, event := range rec.Events {
		if event.Kind != record.KindRecordWarning {
			continue
		}
		warning, decodeErr := record.Decode[record.Warning](event)
		if decodeErr != nil {
			t.Fatalf("decode warning: %v", decodeErr)
		}
		if strings.Contains(warning.Message, "install.log") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("the record says nothing about the artifact it could not keep")
	}
}
