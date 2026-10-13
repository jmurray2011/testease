package runner_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/inputbundle"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/target"
)

// The runner sequences, captures, and records. It reaches no conclusions: it
// says measured, the operation says whatever it says, and nothing here turns
// one into the other.

// --- a target that does only what a test needs it to ---------------------

type fakeSession struct {
	// run is called for every RunScript. It writes whatever output the case
	// wants and returns the result it wants.
	run func(script []byte, args []string, opt target.RunOpts) (target.Result, error)

	fetched []string
	putAt   string
	putBody []byte
	closed  bool
}

func (s *fakeSession) Run(_ context.Context, _ []string, _ target.RunOpts) (target.Result, error) {
	return target.Result{}, errors.New("this test drives RunScript")
}

func (s *fakeSession) RunScript(_ context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	return s.run(script, args, opt)
}

func (s *fakeSession) Fetch(_ context.Context, remote string) (io.ReadCloser, error) {
	s.fetched = append(s.fetched, remote)
	return io.NopCloser(strings.NewReader("artifact body for " + remote)), nil
}

func (s *fakeSession) Put(_ context.Context, body io.Reader, remote string, _ os.FileMode) error {
	s.putAt = remote
	s.putBody, _ = io.ReadAll(body)
	return nil
}

func (s *fakeSession) Close() error {
	s.closed = true
	return nil
}

type fakeTarget struct {
	session *fakeSession

	// change, when set, makes this target a Fingerprinter. Absent, it is not
	// one, and drift becomes a statement about the target rather than a
	// formatting convention.
	change *target.Change

	released      bool
	keptOnRelease bool
}

func (t *fakeTarget) Identity() target.Identity {
	return target.Identity{
		Requested: map[string]any{"provider": "fake", "image": "neutral-base:1"},
		Resolved:  map[string]any{"backend": "fake", "ref": "container:abc123"},
		Facts:     map[string]string{"os": "neutral"},
	}
}

func (t *fakeTarget) Labels() map[string]string {
	return map[string]string{"testease.backend": "fake"}
}

func (t *fakeTarget) Session(context.Context) (target.Session, error) { return t.session, nil }

type printingTarget struct{ *fakeTarget }

func (t *printingTarget) Snapshot(context.Context) (target.Snapshot, error) {
	return target.Snapshot{Instrument: "fake@1", Ref: "before"}, nil
}

func (t *printingTarget) Diff(context.Context, target.Snapshot) (target.Change, error) {
	return *t.change, nil
}

type failingPrintingTarget struct {
	*fakeTarget
	err error
}

func (t *failingPrintingTarget) Snapshot(context.Context) (target.Snapshot, error) {
	return target.Snapshot{}, t.err
}

func (t *failingPrintingTarget) Diff(context.Context, target.Snapshot) (target.Change, error) {
	return target.Change{}, nil
}

type fakeProvider struct {
	target      *fakeTarget
	print       bool
	snapshotErr error
	oneShot     bool
	err         error
}

type mediaTarget struct {
	*fakeTarget
	staged []target.Media
}

func (t *mediaTarget) Stage(_ context.Context, items []target.Media) error {
	t.staged = append(t.staged, items...)
	for _, item := range items {
		if _, err := os.Stat(item.Source); err != nil {
			return fmt.Errorf("staged source was not present: %w", err)
		}
	}
	return nil
}

type mediaProvider struct{ target *mediaTarget }

func (p mediaProvider) Resolve(context.Context, target.Spec) (target.Target, error) {
	return p.target, nil
}

func (p mediaProvider) Release(context.Context, target.Target, bool) error { return nil }

func (p *fakeProvider) Resolve(context.Context, target.Spec) (target.Target, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.print {
		if p.snapshotErr != nil {
			return &failingPrintingTarget{fakeTarget: p.target, err: p.snapshotErr}, nil
		}
		return &printingTarget{p.target}, nil
	}
	if p.oneShot {
		return &oneShotTarget{inner: p.target}, nil
	}
	return p.target, nil
}

func (p *fakeProvider) Release(_ context.Context, _ target.Target, keep bool) error {
	p.target.released = true
	p.target.keptOnRelease = keep
	return nil
}

// --- fixtures -------------------------------------------------------------

func plan(t *testing.T, manifest string) *scenario.Plan {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir probes: %v", err)
	}
	for _, p := range []string{"one.sh", "two.sh"} {
		if err := os.WriteFile(filepath.Join(dir, "probes", p), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
			t.Fatalf("write probe: %v", err)
		}
	}
	m, err := scenario.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, err := m.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return p
}

const oneStep = `
schema: 1
name: one-step
question: does it record what happened?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: do the thing
    host: box
    probe: probes/one.sh
`

func exits(rc int, stdout string) func([]byte, []string, target.RunOpts) (target.Result, error) {
	return func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		if stdout != "" {
			_, _ = io.WriteString(opt.Stdout, stdout)
		}
		return target.Result{RC: rc, Duration: 3 * time.Second}, nil
	}
}

type runOpts struct {
	fingerprint *target.Change
	snapshotErr error
	keep        bool
	ctx         context.Context
	log         *slog.Logger
	inputs      map[string]inputbundle.Resolved
}

func exec(t *testing.T, p *scenario.Plan, session *fakeSession, o runOpts) (*runner.Outcome, *record.Record, error) {
	t.Helper()

	tgt := &fakeTarget{session: session, change: o.fingerprint}
	prov := &fakeProvider{target: tgt, print: o.fingerprint != nil || o.snapshotErr != nil, snapshotErr: o.snapshotErr}

	ctx := o.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := runner.Run(ctx, runner.Options{
		Plan:      p,
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Keep:      o.keep,
		Providers: map[string]target.Provider{"fake": prov},
		Inputs:    o.inputs,
		Log:       o.log,
	})
	if out == nil {
		return nil, nil, err
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read record: %v", readErr)
	}
	return out, rec, err
}

func kinds(rec *record.Record) []record.Kind {
	var out []record.Kind
	for _, e := range rec.Events {
		out = append(out, e.Kind)
	}
	return out
}

func only[T any](t *testing.T, rec *record.Record, kind record.Kind) T {
	t.Helper()

	var found []T
	for _, e := range rec.Events {
		if e.Kind != kind {
			continue
		}
		v, err := record.Decode[T](e)
		if err != nil {
			t.Fatalf("decode %s: %v", kind, err)
		}
		found = append(found, v)
	}
	if len(found) != 1 {
		t.Fatalf("%s events = %d, want 1 (kinds: %v)", kind, len(found), kinds(rec))
	}
	return found[0]
}

// --- the cases ------------------------------------------------------------

func TestARunRecordsWhatItDid(t *testing.T) {
	t.Parallel()

	out, rec, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "all done\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.HasPrefix(filepath.Base(out.Dir), "one-step-") {
		t.Errorf("run dir %q does not carry the scenario name", filepath.Base(out.Dir))
	}
	if out.Status != record.StatusMeasured {
		t.Errorf("status = %q, want %q", out.Status, record.StatusMeasured)
	}
	if rec.Interrupted() {
		t.Error("a run that finished is recorded as interrupted")
	}

	started := only[record.RunStarted](t, rec, record.KindRunStarted)
	if started.Driver != record.DriverManifest {
		t.Errorf("driver = %q, want %q", started.Driver, record.DriverManifest)
	}
	if started.Scenario.Hash == "" || len(started.Scenario.Probes) == 0 {
		t.Errorf("scenario provenance is thin: %+v", started.Scenario)
	}
	if started.Host.PID != os.Getpid() {
		t.Errorf("run-started pid = %d, want %d", started.Host.PID, os.Getpid())
	}

	op := only[record.Operation](t, rec, record.KindOperation)
	if op.RC != 0 {
		t.Errorf("rc = %d, want 0", op.RC)
	}
	if op.Claim.Text != "all done" {
		t.Errorf("claim = %q, want %q", op.Claim.Text, "all done")
	}
	if op.Status != record.StatusMeasured {
		t.Errorf("step status = %q, want %q", op.Status, record.StatusMeasured)
	}
	if op.Stdout != "steps/01-do-the-thing.out" {
		t.Errorf("stdout path = %q", op.Stdout)
	}

	body, readErr := os.ReadFile(filepath.Join(out.Dir, "steps", "01-do-the-thing.out"))
	if readErr != nil {
		t.Fatalf("read step log: %v", readErr)
	}
	if string(body) != "all done\n" {
		t.Errorf("step log = %q, want the output verbatim", body)
	}
}

func TestARunPinsItsResolvedInputIdentity(t *testing.T) {
	t.Parallel()

	want := inputbundle.Resolved{
		Name: "suite", Kind: "git-archive", RequestedPath: "/where/operator/put/suite.tar",
		StagedPath: "/daemon-visible/sha256", SHA256: "sha256:abc", GitCommit: "deadbeef",
		Size: 41922, DescriptorHash: "sha256:def",
	}
	_, rec, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "done\n")}, runOpts{
		inputs: map[string]inputbundle.Resolved{"suite": want},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := only[record.InputResolved](t, rec, record.KindInputResolved)
	if got.Name != want.Name || got.SHA256 != want.SHA256 || got.GitCommit != want.GitCommit || got.DescriptorHash != want.DescriptorHash {
		t.Errorf("input event = %+v, want identity from %+v", got, want)
	}
}

func TestAResolvedInputDigestReachesMediaStaging(t *testing.T) {
	t.Parallel()

	p := plan(t, oneStep)
	host := p.Hosts["box"]
	host.Provider = "docker"
	host.Media = []string{"@suite:/input/suite.tar:ro"}
	p.Hosts["box"] = host
	p.Inputs = map[string]scenario.Input{"suite": {Kind: "file"}}

	staged := filepath.Join(t.TempDir(), "verified-input")
	if err := os.WriteFile(staged, []byte("pinned bytes"), 0o400); err != nil {
		t.Fatal(err)
	}
	want := inputbundle.Resolved{
		Name: "suite", Kind: "file", StagedPath: staged,
		SHA256: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Size:   int64(len("pinned bytes")),
	}
	tgt := &mediaTarget{fakeTarget: &fakeTarget{session: &fakeSession{run: exits(0, "done\n")}}}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan: p, RunsDir: t.TempDir(), RunID: "input-media-digest-20260903T120000Z",
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Inputs:    map[string]inputbundle.Resolved{"suite": want},
		Providers: map[string]target.Provider{"docker": mediaProvider{target: tgt}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tgt.staged) != 1 || tgt.staged[0].SHA256 != want.SHA256 {
		t.Fatalf("staged media = %+v, want digest %s", tgt.staged, want.SHA256)
	}
	rec, err := record.Read(out.Dir)
	if err != nil {
		t.Fatal(err)
	}
	started := only[record.MediaStageStarted](t, rec, record.KindMediaStarted)
	stagedEvent := only[record.MediaStaged](t, rec, record.KindMediaStaged)
	if started.Name != "suite" || started.Size != want.Size || started.SHA256 != want.SHA256 || started.Dest != "/input/suite.tar" {
		t.Errorf("media started = %+v, want portable input identity and destination", started)
	}
	if stagedEvent.Name != started.Name || stagedEvent.Size != started.Size || stagedEvent.SHA256 != started.SHA256 {
		t.Errorf("media staged = %+v, want the verified started identity %+v", stagedEvent, started)
	}
}

func TestADeclaredFileMediaCarriesItsDigestToStaging(t *testing.T) {
	t.Parallel()

	body := []byte("declared media")
	source := filepath.Join(t.TempDir(), "profile.siglae")
	if err := os.WriteFile(source, body, 0o400); err != nil {
		t.Fatal(err)
	}

	p := plan(t, oneStep)
	host := p.Hosts["box"]
	host.Provider = "docker"
	host.Media = []string{source + ":/scenario.siglae:ro"}
	p.Hosts["box"] = host

	tgt := &mediaTarget{fakeTarget: &fakeTarget{session: &fakeSession{run: exits(0, "done\n")}}}
	_, err := runner.Run(context.Background(), runner.Options{
		Plan: p, RunsDir: t.TempDir(), RunID: "declared-file-media-20260903T120000Z",
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"docker": mediaProvider{target: tgt}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	if len(tgt.staged) != 1 || tgt.staged[0].SHA256 != want {
		t.Fatalf("staged media = %+v, want digest %s", tgt.staged, want)
	}
}

func TestASecretWorkspaceIsNamedButRemovedAfterTheRun(t *testing.T) {
	t.Parallel()

	p := plan(t, oneStep)
	p.Secrets = map[string]scenario.SecretFile{"actors": {}}
	out, rec, err := exec(t, p, &fakeSession{run: exits(0, "done\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	started := only[record.RunStarted](t, rec, record.KindRunStarted)
	if !slices.Equal(started.SecretFiles, []string{"actors"}) {
		t.Errorf("secret files = %v", started.SecretFiles)
	}
	runtime := filepath.Join(filepath.Dir(out.Dir), ".testease-runtime")
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("secret workspace survived the run: %v", err)
	}
}

func TestASecretMountUsesTheRuntimePathWithoutRecordingIt(t *testing.T) {
	t.Parallel()

	p := plan(t, oneStep)
	host := p.Hosts["box"]
	host.Provider = "docker"
	host.Media = []string{"%actors:/run/testease/actors.env"}
	p.Hosts["box"] = host
	p.Secrets = map[string]scenario.SecretFile{"actors": {}}
	runsDir := t.TempDir()
	runID := "secret-mount-20260902T120000Z"
	tgt := &mediaTarget{fakeTarget: &fakeTarget{session: &fakeSession{run: exits(0, "done\n")}}}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan: p, RunsDir: runsDir, RunID: runID,
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"docker": mediaProvider{target: tgt}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tgt.staged) != 1 || !strings.Contains(tgt.staged[0].Source, ".testease-runtime") {
		t.Fatalf("staged media = %+v, want the managed runtime path", tgt.staged)
	}
	if _, err := os.Stat(tgt.staged[0].Source); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("managed secret file survived the run: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(out.Dir, record.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(".testease-runtime")) {
		t.Errorf("record exposed the runtime secret path: %s", body)
	}
	if !bytes.Contains(body, []byte(`%actors:/run/testease/actors.env`)) {
		t.Errorf("record lost the logical secret mount: %s", body)
	}
	rec, err := record.Read(out.Dir)
	if err != nil {
		t.Fatal(err)
	}
	started := only[record.MediaStageStarted](t, rec, record.KindMediaStarted)
	if started.Size != 0 || started.SHA256 != "" {
		t.Errorf("secret media exposed size or digest: %+v", started)
	}
}

func TestProgressIsObservedOnlyAfterItsEventWasWritten(t *testing.T) {
	t.Parallel()

	p := plan(t, oneStep)
	runsDir := t.TempDir()
	runID := "one-step-20260902T120000Z"
	tgt := &fakeTarget{session: &fakeSession{run: exits(0, "done\n")}}
	provider := &fakeProvider{target: tgt}
	seen := 0

	_, err := runner.Run(context.Background(), runner.Options{
		Plan: p, RunsDir: runsDir, RunID: runID,
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": provider},
		Observe: func(progress runner.ProgressEvent) {
			body, readErr := os.ReadFile(filepath.Join(runsDir, runID, record.EventsFile))
			if readErr != nil {
				t.Errorf("observer could not read the event file: %v", readErr)
				return
			}
			needle := `"kind":"` + string(progress.Kind) + `"`
			if !bytes.Contains(body, []byte(needle)) {
				t.Errorf("observer saw %s before the record contained it", progress.Kind)
			}
			seen++
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen == 0 {
		t.Fatal("the observer saw no progress")
	}
}

func TestTargetIdentityIsRecordedInThreeLayers(t *testing.T) {
	t.Parallel()

	_, rec, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "x\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A reader must be able to tell "it connected to something" from "this was
	// measured against that exact artifact".
	resolved := only[record.TargetResolved](t, rec, record.KindTargetResolved)
	if resolved.Host != "box" {
		t.Errorf("host = %q, want %q", resolved.Host, "box")
	}
	if resolved.Requested["image"] != "neutral-base:1" {
		t.Errorf("requested = %v", resolved.Requested)
	}
	if resolved.Resolved["ref"] != "container:abc123" {
		t.Errorf("resolved = %v", resolved.Resolved)
	}
	if resolved.Facts["os"] == "" {
		t.Errorf("facts = %v, want something read off the box", resolved.Facts)
	}
}

func TestStepStartedIsWrittenBeforeTheProbeRuns(t *testing.T) {
	t.Parallel()

	// Invariant 1 is structural, not disciplinary: the trace exists before
	// anything can go wrong, so even a kill -9 leaves it.
	var sawItself bool
	session := &fakeSession{}
	session.run = func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		sawItself = true
		return target.Result{RC: 0}, nil
	}

	out, rec, err := exec(t, plan(t, oneStep), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawItself {
		t.Fatal("the probe never ran")
	}
	_ = out

	var stepSeq, opSeq int
	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindStepStarted:
			stepSeq = e.Seq
		case record.KindOperation:
			opSeq = e.Seq
		}
	}
	if stepSeq == 0 || opSeq == 0 || stepSeq > opSeq {
		t.Errorf("step-started at seq %d, operation at seq %d: the trace must come first", stepSeq, opSeq)
	}
}

func TestAProbeThatNeverReportsStillLeavesItsStep(t *testing.T) {
	t.Parallel()

	session := &fakeSession{run: func([]byte, []string, target.RunOpts) (target.Result, error) {
		return target.Result{}, errors.New("the session died mid-step")
	}}

	out, rec, err := exec(t, plan(t, oneStep), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(rec.Events); got == 0 {
		t.Fatal("a step that happened left no trace at all")
	}
	op := only[record.Operation](t, rec, record.KindOperation)
	if op.Status != record.StatusInfraFail {
		t.Errorf("status = %q, want %q", op.Status, record.StatusInfraFail)
	}
	if op.RC != -1 {
		t.Errorf("rc = %d, want -1: a step that never reported has no exit code", op.RC)
	}
	if out.Status != record.StatusInfraFail {
		t.Errorf("run status = %q, want %q", out.Status, record.StatusInfraFail)
	}
}

const twoSteps = `
schema: 1
name: two-steps
question: does a failing step stop the run?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: the thing that fails
    host: box
    probe: probes/one.sh
  - label: the thing that must still run
    host: box
    probe: probes/two.sh
`

func TestAFailingStepDoesNotStopTheRun(t *testing.T) {
	t.Parallel()

	// The end state is wanted however the run went. A scenario asking what an
	// upgrade refusal leaves behind needs the steps after the refusal.
	var ran []string
	session := &fakeSession{}
	session.run = func(script []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		ran = append(ran, string(script))
		if len(ran) == 1 {
			_, _ = io.WriteString(opt.Stdout, "it refused\n")
			return target.Result{RC: 1}, nil
		}
		return target.Result{RC: 0}, nil
	}

	_, rec, err := exec(t, plan(t, twoSteps), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ran) != 2 {
		t.Fatalf("probes run = %d, want 2", len(ran))
	}

	var rcs []int
	for _, e := range rec.Events {
		if e.Kind != record.KindOperation {
			continue
		}
		op, decodeErr := record.Decode[record.Operation](e)
		if decodeErr != nil {
			t.Fatalf("decode: %v", decodeErr)
		}
		rcs = append(rcs, op.RC)
	}
	if len(rcs) != 2 || rcs[0] != 1 || rcs[1] != 0 {
		t.Errorf("rcs = %v, want [1 0]", rcs)
	}
}

const fingerprinted = `
schema: 1
name: fingerprinted
question: did the box change?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: touch nothing
    host: box
    probe: probes/one.sh
    fingerprint: true
`

func TestFingerprintIsRecordedBesideTheClaim(t *testing.T) {
	t.Parallel()

	// "Restore completed successfully!" beside unchanged is the entire point:
	// two independent observations, neither derived from the other.
	session := &fakeSession{run: exits(0, "Restore completed successfully!\r\n")}
	change := &target.Change{Result: target.Unchanged}

	out, rec, err := exec(t, plan(t, fingerprinted), session, runOpts{fingerprint: change})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	op := only[record.Operation](t, rec, record.KindOperation)
	if op.Claim.Text != "Restore completed successfully!\r" {
		t.Errorf("claim = %q, want the CR kept", op.Claim.Text)
	}
	fp := only[record.Fingerprint](t, rec, record.KindFingerprint)
	if fp.Result != record.FingerprintUnchanged {
		t.Errorf("fingerprint = %q, want %q", fp.Result, record.FingerprintUnchanged)
	}
	if fp.Instrument != "fake@1" {
		t.Errorf("instrument = %q, want it named", fp.Instrument)
	}
	_ = out
}

func TestAChangedFingerprintKeepsTheInstrumentsOwnDiff(t *testing.T) {
	t.Parallel()

	change := &target.Change{Result: target.Changed, Detail: []byte("pkg.version 1.0 -> 2.0\n")}
	out, rec, err := exec(t, plan(t, fingerprinted), &fakeSession{run: exits(0, "done\n")}, runOpts{fingerprint: change})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	fp := only[record.Fingerprint](t, rec, record.KindFingerprint)
	if fp.Result != record.FingerprintChanged {
		t.Errorf("fingerprint = %q, want %q", fp.Result, record.FingerprintChanged)
	}
	if fp.Detail == "" {
		t.Fatal("a changed fingerprint kept no evidence of what changed")
	}
	body, readErr := os.ReadFile(filepath.Join(out.Dir, filepath.FromSlash(fp.Detail)))
	if readErr != nil {
		t.Fatalf("read diff artifact: %v", readErr)
	}
	if string(body) != "pkg.version 1.0 -> 2.0\n" {
		t.Errorf("diff artifact = %q, want the instrument's own bytes", body)
	}
}

func TestATargetThatCannotFingerprintSaysSo(t *testing.T) {
	t.Parallel()

	// unavailable is a statement about the target, not a formatting
	// convention, and it is the runner's observation rather than an
	// instrument's - there is no instrument.
	_, rec, err := exec(t, plan(t, fingerprinted), &fakeSession{run: exits(0, "done\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	fp := only[record.Fingerprint](t, rec, record.KindFingerprint)
	if fp.Result != record.FingerprintUnavailable {
		t.Errorf("fingerprint = %q, want %q", fp.Result, record.FingerprintUnavailable)
	}
}

func TestAFingerprintFailureKeepsItsReasonInTheRecord(t *testing.T) {
	t.Parallel()

	_, rec, err := exec(t, plan(t, fingerprinted), &fakeSession{run: exits(0, "done\n")}, runOpts{
		snapshotErr: errors.New("collector exited 2"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	fp := only[record.Fingerprint](t, rec, record.KindFingerprint)
	if fp.Result != record.FingerprintErrored {
		t.Fatalf("fingerprint = %q, want %q", fp.Result, record.FingerprintErrored)
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
		if strings.Contains(warning.Message, "collector exited 2") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("the record says nothing about why fingerprint collection failed")
	}
}

func TestAStepThatAsksForNoFingerprintGetsNoEvent(t *testing.T) {
	t.Parallel()

	_, rec, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "done\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, k := range kinds(rec) {
		if k == record.KindFingerprint {
			t.Fatal("a step that did not ask to be fingerprinted got a fingerprint event")
		}
	}
}

const factual = `
schema: 1
name: factual
question: what did the box say about itself?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: read the facts
    host: box
    probe: probes/one.sh
`

func TestFactsArriveOnTheirOwnChannel(t *testing.T) {
	t.Parallel()

	// Facts are structured data and claims are testimony. They travel apart so
	// that nothing is ever tempted to recover one by parsing the other.
	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "installing\ndone\n")
		_, _ = io.WriteString(opt.Structured, "fact\tpkg.version\t2.0\nfact\tpkg.arch\tx86_64\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := exec(t, plan(t, factual), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	facts := map[string]string{}
	for _, e := range rec.Events {
		if e.Kind != record.KindFact {
			continue
		}
		f, decodeErr := record.Decode[record.Fact](e)
		if decodeErr != nil {
			t.Fatalf("decode fact: %v", decodeErr)
		}
		if f.Value == nil {
			t.Fatalf("fact %q has no value", f.Key)
		}
		facts[f.Key] = *f.Value
	}
	if facts["pkg.version"] != "2.0" || facts["pkg.arch"] != "x86_64" {
		t.Errorf("facts = %v, want both readings", facts)
	}

	op := only[record.Operation](t, rec, record.KindOperation)
	if op.Claim.Text != "done" {
		t.Errorf("claim = %q: the structured channel must not reach the claim", op.Claim.Text)
	}
}

func TestAProbeCanRecordAReadingItCouldNotTake(t *testing.T) {
	t.Parallel()

	// A missing reading is evidence, and the one thing it must never become is
	// a value. A probe that looked at part of a machine has no reading for the
	// whole of it, and saying so is what keeps the cell `unverified` instead of
	// passing on the strength of what it did examine.
	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Structured,
			"fact\tcoverage.examined\talpha\n"+
				"no-reading\tcoverage.machine\tbeta and gamma are present and were not examined\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := exec(t, plan(t, factual), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	facts := map[string]record.Fact{}
	for _, e := range rec.Events {
		if e.Kind != record.KindFact {
			continue
		}
		f, decodeErr := record.Decode[record.Fact](e)
		if decodeErr != nil {
			t.Fatalf("decode fact: %v", decodeErr)
		}
		facts[f.Key] = f
	}

	missing, ok := facts["coverage.machine"]
	if !ok {
		t.Fatal("the unread fact was dropped; a missing reading is evidence and has to be recorded")
	}
	if missing.Value != nil {
		t.Errorf("value = %q, want none: an unread fact must never acquire a value", *missing.Value)
	}
	if !strings.Contains(missing.Error, "beta and gamma") {
		t.Errorf("error = %q, want it to carry why the reading is missing", missing.Error)
	}
	if got := facts["coverage.examined"]; got.Value == nil || *got.Value != "alpha" {
		t.Errorf("coverage.examined = %v, want the ordinary fact alongside it", got.Value)
	}
}

func TestAProbeMaySelectItsOwnClaim(t *testing.T) {
	t.Parallel()

	// The last-non-empty-line default is a shell convention, not a property of
	// operations. A probe that knows its tool's output may select better - but
	// it selects; it does not transform.
	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "the decisive line\nsome trailing noise\n")
		_, _ = io.WriteString(opt.Structured, "claim\tthe decisive line\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := exec(t, plan(t, factual), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	op := only[record.Operation](t, rec, record.KindOperation)
	if op.Claim.Text != "the decisive line" {
		t.Errorf("claim = %q, want the probe's own selection", op.Claim.Text)
	}
	if op.Claim.Extractor == record.ExtractorLastLine {
		t.Errorf("extractor = %q, want it to name the probe's selection", op.Claim.Extractor)
	}
}

func TestAnUnreadableStructuredLineIsRecordedNotDropped(t *testing.T) {
	t.Parallel()

	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "done\n")
		_, _ = io.WriteString(opt.Structured, "this is not a fact line\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := exec(t, plan(t, factual), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var warned bool
	for _, e := range rec.Events {
		if e.Kind == record.KindRecordWarning {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a malformed structured line left no warning: %v", kinds(rec))
	}
}

const controlled = `
schema: 1
name: controlled
question: do the instruments work?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: CONTROL negative
    host: box
    probe: probes/one.sh
    control: negative
    fingerprint: true
    expect: {fingerprint: unchanged}
`

func TestAControlIsCheckedAtRunTime(t *testing.T) {
	t.Parallel()

	_, rec, err := exec(t, plan(t, controlled), &fakeSession{run: exits(0, "nothing\n")},
		runOpts{fingerprint: &target.Change{Result: target.Unchanged}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	checked := only[record.ControlChecked](t, rec, record.KindControlChecked)
	if !checked.OK {
		t.Errorf("control checked = %+v, want it to hold", checked)
	}
	if checked.Expected["fingerprint"] != "unchanged" || checked.Observed["fingerprint"] != "unchanged" {
		t.Errorf("control checked = %+v, want both sides recorded", checked)
	}
}

func TestAControlThatFailsIsRecordedAndTheRunContinues(t *testing.T) {
	t.Parallel()

	out, rec, err := exec(t, plan(t, controlled), &fakeSession{run: exits(0, "nothing\n")},
		runOpts{fingerprint: &target.Change{Result: target.Changed}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	checked := only[record.ControlChecked](t, rec, record.KindControlChecked)
	if checked.OK {
		t.Error("a negative control that drifted was recorded as holding")
	}
	// A broken control is a finding, not an infrastructure failure: the run
	// measured exactly what it set out to measure.
	if out.Status != record.StatusMeasured {
		t.Errorf("run status = %q, want %q", out.Status, record.StatusMeasured)
	}
}

const needsMore = `
schema: 1
name: needs-more
question: can this target even do it?
requires: [fingerprint]
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: do the thing
    host: box
    probe: probes/one.sh
`

func TestAMissingCapabilityRefusesEarlyAndNamesTheRemedy(t *testing.T) {
	t.Parallel()

	out, rec, err := exec(t, plan(t, needsMore), &fakeSession{run: exits(0, "x\n")}, runOpts{})
	if err == nil {
		t.Fatal("the runner ran a scenario against a target that cannot answer it")
	}
	var precondition *runner.PreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("error is %T (%v), want *runner.PreconditionError", err, err)
	}
	if precondition.Remedy == "" {
		t.Error("a precondition failure printed no remedy")
	}

	// The refusal is itself evidence: something was resolved, and what it
	// could not do is on the record.
	for _, k := range kinds(rec) {
		if k == record.KindOperation {
			t.Fatal("a step ran after the capability check refused")
		}
	}
	if out.Status != record.StatusInfraFail {
		t.Errorf("status = %q, want %q", out.Status, record.StatusInfraFail)
	}
}

func TestAnInterruptedRunClosesItsRecord(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	session := &fakeSession{}
	session.run = func([]byte, []string, target.RunOpts) (target.Result, error) {
		cancel()
		return target.Result{RC: 0}, nil
	}

	out, rec, err := exec(t, plan(t, twoSteps), session, runOpts{ctx: ctx})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if out.Status != record.StatusInterrupted {
		t.Errorf("status = %q, want %q", out.Status, record.StatusInterrupted)
	}
	// Stop scheduling, but close the record: an interrupted run that never
	// says so is indistinguishable from a complete one.
	if rec.Interrupted() {
		t.Error("the record has no run-finished at all")
	}
	finished := only[record.RunFinished](t, rec, record.KindRunFinished)
	if finished.Status != record.StatusInterrupted {
		t.Errorf("run-finished status = %q, want %q", finished.Status, record.StatusInterrupted)
	}

	var ops int
	for _, k := range kinds(rec) {
		if k == record.KindOperation {
			ops++
		}
	}
	if ops != 1 {
		t.Errorf("operations = %d, want 1: the second step must not be scheduled", ops)
	}
}

const withArtifact = `
schema: 1
name: with-artifact
question: does the file come back?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: write a file
    host: box
    probe: probes/one.sh
artifacts:
  - {step: write a file, from: /tmp/attempt.out, as: attempt.out, when: always}
`

func TestArtifactsComeBack(t *testing.T) {
	t.Parallel()

	session := &fakeSession{run: exits(0, "done\n")}
	out, _, err := exec(t, plan(t, withArtifact), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(session.fetched) != 1 || session.fetched[0] != "/tmp/attempt.out" {
		t.Fatalf("fetched = %v, want /tmp/attempt.out", session.fetched)
	}
	body, readErr := os.ReadFile(filepath.Join(out.Dir, "artifacts", "attempt.out"))
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if !strings.Contains(string(body), "/tmp/attempt.out") {
		t.Errorf("artifact = %q", body)
	}
}

func TestTargetsAreReleasedAndKeepIsHonoured(t *testing.T) {
	t.Parallel()

	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%v", keep), func(t *testing.T) {
			t.Parallel()

			tgt := &fakeTarget{session: &fakeSession{run: exits(0, "x\n")}}
			prov := &fakeProvider{target: tgt}
			_, err := runner.Run(context.Background(), runner.Options{
				Plan:      plan(t, oneStep),
				RunsDir:   t.TempDir(),
				Tool:      record.ToolRef{Version: "0.0.0-test"},
				Keep:      keep,
				Providers: map[string]target.Provider{"fake": prov},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !tgt.released {
				t.Error("the target was never released")
			}
			if tgt.keptOnRelease != keep {
				t.Errorf("released with keep=%v, want %v", tgt.keptOnRelease, keep)
			}
		})
	}
}

func TestFailedTargetsAreRetainedUnlessDisposablePolicyReleasesThem(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name                 string
		releaseFailedTargets bool
		wantKept             bool
	}{
		{name: "forensic default", wantKept: true},
		{name: "disposable capacity", releaseFailedTargets: true, wantKept: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tgt := &fakeTarget{session: &fakeSession{run: func([]byte, []string, target.RunOpts) (target.Result, error) {
				return target.Result{}, errors.New("the session died mid-step")
			}}}
			prov := &fakeProvider{target: tgt}
			out, err := runner.Run(context.Background(), runner.Options{
				Plan:                 plan(t, oneStep),
				RunsDir:              t.TempDir(),
				Tool:                 record.ToolRef{Version: "0.0.0-test"},
				ReleaseFailedTargets: test.releaseFailedTargets,
				Providers:            map[string]target.Provider{"fake": prov},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if out.Status != record.StatusInfraFail {
				t.Fatalf("status = %q, want infra-fail", out.Status)
			}
			if !tgt.released || tgt.keptOnRelease != test.wantKept {
				t.Fatalf("released=%v kept=%v, want released=true kept=%v", tgt.released, tgt.keptOnRelease, test.wantKept)
			}
		})
	}
}

func TestAProviderThatCannotResolveIsRecordedNotHidden(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{target: &fakeTarget{session: &fakeSession{}}, err: errors.New("no such image")}
	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, oneStep),
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": prov},
	})
	if err == nil {
		t.Fatal("a target that could not be resolved ran anyway")
	}
	if out == nil {
		t.Fatal("no record was left behind")
	}

	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	resolved := only[record.TargetResolved](t, rec, record.KindTargetResolved)
	if resolved.Resolved["error"] == nil {
		t.Errorf("target-resolved = %+v, want the failure recorded in the layer that failed", resolved)
	}
	if out.Status != record.StatusInfraFail {
		t.Errorf("status = %q, want %q", out.Status, record.StatusInfraFail)
	}
}

func TestDiagnosticsCarryTheRunAndTheStep(t *testing.T) {
	t.Parallel()

	// Diagnostics are a fourth channel, apart from evidence, from a record's
	// notices, and from probe output. They exist so a CI log can be searched
	// against a record, which needs both keys on every line.
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

	session := &fakeSession{run: func([]byte, []string, target.RunOpts) (target.Result, error) {
		return target.Result{}, errors.New("the session died mid-step")
	}}
	out, _, err := exec(t, plan(t, oneStep), session, runOpts{log: logger})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logged.String()), "\n") {
		var entry map[string]any
		if line == "" {
			continue
		}
		if unmarshalErr := json.Unmarshal([]byte(line), &entry); unmarshalErr != nil {
			t.Fatalf("log line is not json: %v (%q)", unmarshalErr, line)
		}
		if entry["level"] != "WARN" {
			continue
		}
		if entry["run"] != out.RunID {
			t.Errorf("warning has run=%v, want %q", entry["run"], out.RunID)
		}
		if entry["step"] == nil {
			t.Errorf("a step's warning does not say which step: %v", entry)
		}
		found = true
	}
	if !found {
		t.Errorf("a step that never reported logged no warning: %q", logged.String())
	}
}

func TestTheLoggerIsOptional(t *testing.T) {
	t.Parallel()

	// A nil logger must be as safe as any other. Every other seam in this
	// package treats the zero value as usable, and a run must not depend on
	// having somewhere to complain to.
	if _, _, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "x\n")}, runOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

const judged = `
schema: 1
name: judged
question: how is this record meant to be read?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: do the thing
    host: box
    probe: probes/one.sh
judge: {name: expect, version: "1"}
`

func TestARecordSaysHowItIsMeantToBeRead(t *testing.T) {
	t.Parallel()

	// Judging a two-year-old record must need the record and nothing else -
	// the scenario tree it came from may be long gone. The same reason the
	// expect judge takes its declarations from the events rather than the
	// manifest.
	_, rec, err := exec(t, plan(t, judged), &fakeSession{run: exits(0, "done\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	started := only[record.RunStarted](t, rec, record.KindRunStarted)
	if started.Judge == nil {
		t.Fatal("the record does not say which judge the scenario declared")
	}
	if started.Judge.Name != "expect" || started.Judge.Version != "1" {
		t.Errorf("judge = %+v, want expect@1", started.Judge)
	}
}

func TestAScenarioThatDeclaresNoJudgeSaysNothing(t *testing.T) {
	t.Parallel()

	_, rec, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "done\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if started := only[record.RunStarted](t, rec, record.KindRunStarted); started.Judge != nil {
		t.Errorf("judge = %+v, want nothing invented", started.Judge)
	}
}

func TestMediaKeepsAWindowsSourcePathWhole(t *testing.T) {
	t.Parallel()

	// A drive letter is not a separator. Splitting on every colon turns
	// C:\store into source "C", which then fails as a mount nobody typed.
	for _, c := range []struct {
		spec     string
		src, dst string
		readOnly bool
	}{
		{`/srv/store:/media:ro`, "/srv/store", "/media", true},
		{`/srv/store:/media`, "/srv/store", "/media", false},
		{`C:\store:C:\media:ro`, `C:\store`, `C:\media`, true},
		{`C:/store:/media`, `C:/store`, "/media", false},
	} {
		got := runner.ParseMedia(c.spec)
		if got.Source != c.src || got.Dest != c.dst || got.ReadOnly != c.readOnly {
			t.Errorf("ParseMedia(%q) = %+v, want %s -> %s ro=%v", c.spec, got, c.src, c.dst, c.readOnly)
		}
	}
}

const expectsAFact = `
schema: 1
name: expects-a-fact
question: what does the box say about itself?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: read the state
    host: box
    probe: probes/one.sh
    expect:
      rc: 0
      fact.svc.active: active
`

func TestAFactExpectationReachesTheRecord(t *testing.T) {
	t.Parallel()

	// The judge reads what a step declared out of the record, not out of the
	// manifest: a verdict years later needs the record and nothing else. A fact
	// expectation the runner never wrote down is a check the scenario believes
	// is running and no judge can see.
	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "done\n")
		_, _ = io.WriteString(opt.Structured, "fact\tsvc.active\tactive\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := exec(t, plan(t, expectsAFact), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Against record.FactPrefix rather than a literal, because the manifest
	// spells the prefix from its own side: the two are joined by nothing but
	// agreement, and a drift between them turns a declared check into a field
	// nobody recognises without anything failing.
	field := record.FactPrefix + "svc.active"
	checked := only[record.ControlChecked](t, rec, record.KindControlChecked)
	if got := checked.Expected[field]; got != "active" {
		t.Errorf("expected = %v, want the fact expectation declared", checked.Expected)
	}
	if got := checked.Observed[field]; got != "active" {
		t.Errorf("observed = %v, want the runner's own reading beside it", checked.Observed)
	}
	if !checked.OK {
		t.Errorf("control checked = %+v, want it to hold", checked)
	}
}

func TestAFactExpectationTheProbeDidNotAnswerFailsTheRunTimeCheck(t *testing.T) {
	t.Parallel()

	// Fail-fast visibility: the person watching wants to know the reading never
	// arrived. What that means is the judge's call - it says unverifiable, not
	// regressed - and the run carries on either way.
	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "done\n")
		return target.Result{RC: 0}, nil
	}}

	out, rec, err := exec(t, plan(t, expectsAFact), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	checked := only[record.ControlChecked](t, rec, record.KindControlChecked)
	if checked.OK {
		t.Error("an expectation with no reading behind it was recorded as holding")
	}
	if _, recorded := checked.Observed[record.FactPrefix+"svc.active"]; recorded {
		t.Errorf("observed = %v, want a reading nobody took left absent rather than blank", checked.Observed)
	}
	if out.Status != record.StatusMeasured {
		t.Errorf("run status = %q, want %q: a missing reading is a finding, not a substrate failure", out.Status, record.StatusMeasured)
	}
}

func TestACrlfLineTerminatorIsNotPartOfTheReading(t *testing.T) {
	t.Parallel()

	// A PowerShell probe writes CRLF, because that is what a line ending is on
	// Windows. The terminator is not part of the value: a fact of "yes\r" would
	// never equal the "yes" a scenario declared, and every Windows scenario
	// would fail on an invisible character.
	//
	// A claim is different and stays verbatim - the CRLF lesson from the corpus
	// is about the text a tool printed, not about how a line ends.
	session := &fakeSession{run: func(_ []byte, _ []string, opt target.RunOpts) (target.Result, error) {
		_, _ = io.WriteString(opt.Stdout, "done\n")
		_, _ = io.WriteString(opt.Structured, "fact\tsvc.active\tactive\r\nfact\tpkg.version\t7.2.2\r\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := exec(t, plan(t, factual), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, e := range rec.Events {
		if e.Kind != record.KindFact {
			continue
		}
		f, err := record.Decode[record.Fact](e)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Value == nil || strings.ContainsRune(*f.Value, '\r') {
			t.Errorf("fact %s = %q, want the line terminator gone", f.Key, *f.Value)
		}
	}
}

// oneShotTarget has no session at all: every command is its own container, so
// nothing one step does is visible to the next. It deliberately does not
// implement Sessioner, because that is the whole difference.
type oneShotTarget struct {
	inner *fakeTarget
}

func (t *oneShotTarget) Identity() target.Identity { return t.inner.Identity() }
func (t *oneShotTarget) Labels() map[string]string { return t.inner.Labels() }

func (t *oneShotTarget) RunOnce(_ context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	return t.inner.session.run(script, args, opt)
}

const twoStepsOneHost = `
schema: 1
name: two-steps
question: does the second step see what the first did?
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: do a thing
    host: box
    probe: probes/one.sh
  - label: look for what the first step did
    host: box
    probe: probes/two.sh
`

func TestAHostWithNoSessionSaysStepsDoNotShareState(t *testing.T) {
	t.Parallel()

	// The failure this exists to stop: a scenario whose steps depend on each
	// other, run against a target that starts fresh every time. It measures a
	// pristine machine from step two on and the record looks clean, because
	// every step really did run and really did report.
	tgt := &fakeTarget{session: &fakeSession{run: exits(0, "done\n")}}
	prov := &fakeProvider{target: tgt, oneShot: true}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, twoStepsOneHost),
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": prov},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rec, err := record.Read(out.Dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var said bool
	for _, e := range rec.Events {
		if e.Kind != record.KindRecordWarning {
			continue
		}
		w, err := record.Decode[record.Warning](e)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.Contains(w.Message, "share") || strings.Contains(w.Message, "session") {
			said = true
		}
	}
	if !said {
		t.Error("a multi-step scenario on a session-less host left no warning in the record")
	}
}
