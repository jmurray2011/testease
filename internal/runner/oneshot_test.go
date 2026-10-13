package runner_test

import (
	"context"
	"io"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

// A one-shot target has no life between steps: the container IS the command.
// That is a different capability from a session, not a flavour of one.

const oneShotScenario = `
schema: 1
name: one-shot
question: what does the installer do?
requires: [oneshot]
hosts:
  box: {provider: fake, image: neutral-windows:1}
steps:
  - label: run the installer
    host: box
    probe: probes/one.sh
    args: ["3.0"]
`

type shotTarget struct {
	ran  [][]string
	run  func(script []byte, args []string, opt target.RunOpts) (target.Result, error)
	spec target.Spec
}

func (t *shotTarget) Identity() target.Identity {
	return target.Identity{
		Requested: map[string]any{"image": "neutral-windows:1"},
		Resolved:  map[string]any{"backend": "one-shot"},
		Facts:     map[string]string{"os": "neutral-windows"},
	}
}

func (t *shotTarget) Labels() map[string]string {
	return map[string]string{"testease.backend": "one-shot"}
}

func (t *shotTarget) RunOnce(_ context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	t.ran = append(t.ran, args)
	if t.run != nil {
		return t.run(script, args, opt)
	}
	_, _ = io.WriteString(opt.Stdout, "installed\n")
	return target.Result{RC: 0}, nil
}

type shotProvider struct{ target *shotTarget }

func (p *shotProvider) Resolve(_ context.Context, spec target.Spec) (target.Target, error) {
	p.target.spec = spec
	return p.target, nil
}
func (p *shotProvider) Release(context.Context, target.Target, bool) error { return nil }

func runOneShot(t *testing.T, tgt *shotTarget) (*runner.Outcome, *record.Record, error) {
	t.Helper()

	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, oneShotScenario),
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": &shotProvider{target: tgt}},
	})
	if out == nil {
		return nil, nil, err
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	return out, rec, err
}

func TestAOneShotTargetRunsItsProbeAsOneCommand(t *testing.T) {
	t.Parallel()

	tgt := &shotTarget{}
	_, rec, err := runOneShot(t, tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(tgt.ran) != 1 {
		t.Fatalf("one-shot runs = %d, want 1", len(tgt.ran))
	}
	if len(tgt.ran[0]) != 1 || tgt.ran[0][0] != "3.0" {
		t.Errorf("args = %v, want the step's own", tgt.ran[0])
	}

	op := only[record.Operation](t, rec, record.KindOperation)
	if op.Status != record.StatusMeasured || op.Claim.Text != "installed" {
		t.Errorf("operation = %+v", op)
	}
}

func TestAOneShotBackendIsToldWhatItIsAboutToRun(t *testing.T) {
	t.Parallel()

	// A backend with no session has to know the probe before it creates
	// anything: the container is the command, and there is nothing to exec
	// into once it exists.
	tgt := &shotTarget{}
	if _, _, err := runOneShot(t, tgt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tgt.spec.Probe != "probes/one.sh" {
		t.Errorf("spec.Probe = %q, want the probe the step names", tgt.spec.Probe)
	}
}

func TestAOneShotCellReportsReadingsOnTheStructuredChannel(t *testing.T) {
	t.Parallel()

	// A container cannot be handed fd 3 the way a shell can, so its readings
	// come back as a file - and arrive on the same channel all the same, so a
	// judge cannot tell which transport carried them.
	tgt := &shotTarget{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "done\n")
		_, _ = io.WriteString(opt.Structured, "fact\tpkg.version\t3.0\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := runOneShot(t, tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := factIn(t, rec, "pkg.version"); got != "3.0" {
		t.Errorf("pkg.version = %q, want the cell's own reading", got)
	}
}

func TestASessionTargetIsNeverRunAsOneShot(t *testing.T) {
	t.Parallel()

	// A target that has a session keeps state between steps. Running its steps
	// as one-shot cells would measure a different machine each time.
	session := &fakeSession{run: exits(0, "measured on the session\n")}
	prov := &fakeProvider{target: &fakeTarget{session: session}}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, oneStep),
		RunsDir:   t.TempDir(),
		Providers: map[string]target.Provider{"fake": prov},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	if op := only[record.Operation](t, rec, record.KindOperation); op.Claim.Text != "measured on the session" {
		t.Errorf("claim = %q, want the session to have run it", op.Claim.Text)
	}
}
