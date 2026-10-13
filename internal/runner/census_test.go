package runner_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/target"
)

// A census is one run over many cells. The parent record carries the scope and
// the scheduling; each cell is a self-contained sub-record, and a cell is never
// independently citable - the citable id is the parent's.

const sweep = `
schema: 1
name: sweep
question: what does each version do?
params:
  from: {values: "1.0,2.0,3.0", axis: true}
hosts:
  box: {provider: fake, image: neutral-base:1}
steps:
  - label: install
    host: box
    probe: probes/one.sh
    args: ["{{.from}}"]
`

// census builds a manifest and runs it over a scope.
type census struct {
	manifest string
	values   []string
	parallel int
	resume   string
	runsDir  string
	perCell  func(from string) (target.Result, error)
	ctx      context.Context
}

// cellProvider hands every cell its own target, the way a real one does.
type cellProvider struct {
	mu       sync.Mutex
	resolved []string
	live     int
	mostLive int
	perCell  func(from string) (target.Result, error)
}

func (p *cellProvider) Resolve(_ context.Context, spec target.Spec) (target.Target, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// A cell's targets must be its own: two cells sharing a container name
	// would measure each other's machine.
	name := spec.Cell + "/" + spec.Host
	for _, seen := range p.resolved {
		if seen == name {
			return nil, errors.New("two cells asked for the same target: " + name)
		}
	}
	p.resolved = append(p.resolved, name)

	p.live++
	if p.live > p.mostLive {
		p.mostLive = p.live
	}
	session := &fakeSession{}
	session.run = func(_ []byte, args []string, opt target.RunOpts) (target.Result, error) {
		from := ""
		if len(args) > 0 {
			from = args[0]
		}
		if p.perCell != nil {
			return p.perCell(from)
		}
		_, _ = io.WriteString(opt.Stdout, "installed "+from+"\n")
		return target.Result{RC: 0}, nil
	}
	return &fakeTarget{session: session}, nil
}

func (p *cellProvider) Release(context.Context, target.Target, bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live--
	return nil
}

func sweepRun(t *testing.T, c census) (*runner.Outcome, *record.Record, *cellProvider, error) {
	t.Helper()

	manifest := c.manifest
	if manifest == "" {
		manifest = sweep
	}
	m := manifestOf(t, manifest)

	values := c.values
	if values == nil {
		values = []string{"1.0", "2.0", "3.0"}
	}
	runsDir := c.runsDir
	if runsDir == "" {
		runsDir = t.TempDir()
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	parallel := c.parallel
	if parallel == 0 {
		parallel = 1
	}

	prov := &cellProvider{perCell: c.perCell}
	opt := runner.Options{
		Manifest:  m,
		RunsDir:   runsDir,
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Parallel:  parallel,
		Resume:    c.resume,
		Providers: map[string]target.Provider{"fake": prov},
	}
	if c.resume == "" {
		opt.Scope = &runner.Scope{Axes: map[string][]string{"from": values}, Source: "cli"}
	}

	out, err := runner.Run(ctx, opt)
	if out == nil {
		return nil, nil, prov, err
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read parent: %v", readErr)
	}
	return out, rec, prov, err
}

func manifestOf(t *testing.T, body string) *scenario.Manifest {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(body), 0o644); err != nil {
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
	return m
}

func cellStatuses(t *testing.T, rec *record.Record) map[string]record.Status {
	t.Helper()

	out := map[string]record.Status{}
	for _, e := range rec.Events {
		if e.Kind != record.KindCellFinished {
			continue
		}
		finished, err := record.Decode[record.CellFinished](e)
		if err != nil {
			t.Fatalf("decode cell-finished: %v", err)
		}
		out[finished.Key] = finished.Status
	}
	return out
}

func TestACensusWritesAParentAndASubRecordPerCell(t *testing.T) {
	t.Parallel()

	out, rec, _, err := sweepRun(t, census{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := cellStatuses(t, rec); len(got) != 3 {
		t.Fatalf("cells finished = %v, want 3", got)
	}
	for _, key := range []string{"1.0", "2.0", "3.0"} {
		cellDir := filepath.Join(out.Dir, "cells", key)
		cell, readErr := record.Read(cellDir)
		if readErr != nil {
			t.Fatalf("read cell %s: %v", key, readErr)
		}
		// Self-contained: a cell's own record says what it ran and what
		// happened, without the parent open beside it.
		op := only[record.Operation](t, cell, record.KindOperation)
		if op.Status != record.StatusMeasured {
			t.Errorf("cell %s step status = %q", key, op.Status)
		}
		started := only[record.RunStarted](t, cell, record.KindRunStarted)
		if started.Params["from"] != key {
			t.Errorf("cell %s records params %v", key, started.Params)
		}
		if started.RunID != out.RunID {
			t.Errorf("cell %s claims run id %q, want the parent's %q: a cell is not separately citable",
				key, started.RunID, out.RunID)
		}
	}
}

func TestTheParentRecordsTheScopeAndWhereItCameFrom(t *testing.T) {
	t.Parallel()

	// Two people with different stores get differently scoped sweeps whose
	// records read identically unless the set is written down.
	_, rec, _, err := sweepRun(t, census{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	started := only[record.RunStarted](t, rec, record.KindRunStarted)
	if started.Matrix == nil {
		t.Fatal("the parent does not record its scope")
	}
	if got := strings.Join(started.Matrix.Axes["from"], ","); got != "1.0,2.0,3.0" {
		t.Errorf("axes = %s", got)
	}
	if started.Matrix.Source != "cli" {
		t.Errorf("source = %q, want it recorded", started.Matrix.Source)
	}
	if started.Matrix.ResumeOf != "" {
		t.Errorf("a first pass claims to be a resume of %q", started.Matrix.ResumeOf)
	}
}

func TestTheParentCountsHowTheCensusClosed(t *testing.T) {
	t.Parallel()

	out, rec, _, err := sweepRun(t, census{
		perCell: func(from string) (target.Result, error) {
			if from == "2.0" {
				return target.Result{}, errors.New("the session died")
			}
			return target.Result{RC: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	finished := only[record.RunFinished](t, rec, record.KindRunFinished)
	if finished.Cells[record.StatusMeasured] != 2 || finished.Cells[record.StatusInfraFail] != 1 {
		t.Errorf("cells = %v, want 2 measured and 1 infra-fail", finished.Cells)
	}
	// A census that closes with unmeasured cells says so, loudly: the record
	// has to distinguish measured from merely accounted for.
	if out.Measured != 2 || out.Total != 3 {
		t.Errorf("outcome = %d of %d measured, want 2 of 3", out.Measured, out.Total)
	}
	if out.Status == record.StatusMeasured {
		t.Error("a census with an unmeasured cell closed as measured")
	}
}

func TestOneCellFailingDoesNotStopTheCensus(t *testing.T) {
	t.Parallel()

	_, rec, _, err := sweepRun(t, census{
		perCell: func(from string) (target.Result, error) {
			if from == "1.0" {
				return target.Result{}, errors.New("no boot")
			}
			return target.Result{RC: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := cellStatuses(t, rec)
	if len(got) != 3 {
		t.Fatalf("cells finished = %v, want all three accounted for", got)
	}
	if got["2.0"] != record.StatusMeasured || got["3.0"] != record.StatusMeasured {
		t.Errorf("cells = %v, want the later cells measured", got)
	}
}

func TestCellsGetTheirOwnTargets(t *testing.T) {
	t.Parallel()

	_, _, prov, err := sweepRun(t, census{parallel: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.resolved) != 3 {
		t.Errorf("targets resolved = %v, want one per cell", prov.resolved)
	}
}

func TestParallelismIsBounded(t *testing.T) {
	t.Parallel()

	_, _, prov, err := sweepRun(t, census{values: []string{"1.0", "2.0", "3.0", "4.0", "5.0"}, parallel: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.mostLive > 2 {
		t.Errorf("%d cells were live at once, want at most 2", prov.mostLive)
	}
}

func TestResumeCoversExactlyWhatWasNotMeasured(t *testing.T) {
	t.Parallel()

	runsDir := t.TempDir()
	first, _, _, err := sweepRun(t, census{
		runsDir: runsDir,
		perCell: func(from string) (target.Result, error) {
			if from == "2.0" {
				return target.Result{}, errors.New("no boot")
			}
			return target.Result{RC: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}

	second, rec, prov, err := sweepRun(t, census{runsDir: runsDir, resume: first.RunID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.Dir != first.Dir {
		t.Errorf("a resume wrote a new record at %s, want it to continue %s", second.Dir, first.Dir)
	}
	if len(prov.resolved) != 1 {
		t.Errorf("resolved %v, want only the cell that was not measured", prov.resolved)
	}

	// Lineage is appended, never overwritten: a resumed census can never be
	// mistaken for a single complete one.
	var starts []record.RunStarted
	for _, e := range rec.Events {
		if e.Kind != record.KindRunStarted {
			continue
		}
		started, decodeErr := record.Decode[record.RunStarted](e)
		if decodeErr != nil {
			t.Fatalf("decode: %v", decodeErr)
		}
		starts = append(starts, started)
	}
	if len(starts) != 2 {
		t.Fatalf("run-started events = %d, want the first pass and the resume", len(starts))
	}
	if starts[0].Matrix.ResumeOf != "" {
		t.Errorf("the first pass claims to be a resume")
	}
	if starts[1].Matrix.ResumeOf != first.RunID {
		t.Errorf("the resume points at %q, want %q", starts[1].Matrix.ResumeOf, first.RunID)
	}
	if got := strings.Join(starts[1].Matrix.Axes["from"], ","); got != "1.0,2.0,3.0" {
		t.Errorf("resumed scope = %s, want the set the first pass wrote down", got)
	}

	// And the cell that measured the first time was left exactly as it was.
	if got := cellStatuses(t, rec)["2.0"]; got != record.StatusMeasured {
		t.Errorf("cell 2.0 = %q after the resume, want it measured", got)
	}
}

func TestResumeLeavesTheCellsItDidNotTouch(t *testing.T) {
	t.Parallel()

	runsDir := t.TempDir()
	first, _, _, err := sweepRun(t, census{
		runsDir: runsDir,
		perCell: func(from string) (target.Result, error) {
			if from == "3.0" {
				return target.Result{}, errors.New("no boot")
			}
			return target.Result{RC: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}

	before, err := os.ReadFile(filepath.Join(first.Dir, "cells", "1.0", record.EventsFile))
	if err != nil {
		t.Fatalf("read cell: %v", err)
	}
	if _, _, _, err := sweepRun(t, census{runsDir: runsDir, resume: first.RunID}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(first.Dir, "cells", "1.0", record.EventsFile))
	if err != nil {
		t.Fatalf("read cell again: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a resume rewrote the evidence of a cell it was not re-running")
	}
}

func TestResumingACompleteCensusRunsNothing(t *testing.T) {
	t.Parallel()

	runsDir := t.TempDir()
	first, _, _, err := sweepRun(t, census{runsDir: runsDir})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}

	out, _, prov, err := sweepRun(t, census{runsDir: runsDir, resume: first.RunID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(prov.resolved) != 0 {
		t.Errorf("a complete census booted %v on resume", prov.resolved)
	}
	if out.Status != record.StatusMeasured {
		t.Errorf("status = %q, want %q", out.Status, record.StatusMeasured)
	}
}

func TestResumingSomethingThatIsNotACensus(t *testing.T) {
	t.Parallel()

	runsDir := t.TempDir()
	_, err := runner.Run(context.Background(), runner.Options{
		Manifest:  manifestOf(t, sweep),
		RunsDir:   runsDir,
		Resume:    "no-such-run-20260101T000000Z",
		Providers: map[string]target.Provider{"fake": &cellProvider{}},
	})
	if err == nil {
		t.Fatal("resumed a run that does not exist")
	}
	if !strings.Contains(err.Error(), "no-such-run") {
		t.Errorf("error %q does not name what it could not find", err)
	}
}

func TestAnInterruptedCensusStopsSchedulingAndSaysSo(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	out, rec, _, err := sweepRun(t, census{
		ctx: ctx,
		perCell: func(string) (target.Result, error) {
			once.Do(cancel)
			return target.Result{RC: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if out.Status != record.StatusInterrupted {
		t.Errorf("status = %q, want %q", out.Status, record.StatusInterrupted)
	}
	if rec.Interrupted() {
		t.Error("the parent record has no run-finished at all")
	}
	if got := len(cellStatuses(t, rec)); got >= 3 {
		t.Errorf("cells finished = %d, want scheduling to have stopped", got)
	}
}

func TestCellKeysAreTheDirectoryNames(t *testing.T) {
	t.Parallel()

	out, _, _, err := sweepRun(t, census{values: []string{"1.0", "2.0"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(out.Dir, "cells"))
	if err != nil {
		t.Fatalf("read cells: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "1.0,2.0" {
		t.Errorf("cells/ holds %v", names)
	}
}

func TestASingleTargetRunStillHasNoCells(t *testing.T) {
	t.Parallel()

	out, _, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "x\n")}, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out.Dir, "cells")); !os.IsNotExist(err) {
		t.Errorf("a single-target run has a cells/ directory (err = %v)", err)
	}
}

const sweptWithAParam = `
schema: 1
name: swept-with-a-param
question: does a cell get the run's parameters as well as its own?
params:
  from: {values: "1.0,2.0", axis: true}
  image: {default: "neutral-base:1"}
hosts:
  box: {provider: fake, image: "{{.image}}"}
steps:
  - label: install
    host: box
    probe: probes/one.sh
    args: ["{{.from}}", "{{.image}}"]
`

func TestACellGetsTheRunsParametersAsWellAsItsOwn(t *testing.T) {
	t.Parallel()

	// A parameter given on the command line applies to every cell. Dropping it
	// silently would run the census over defaults nobody asked for and record
	// it as though the override had been honoured.
	m := manifestOf(t, sweptWithAParam)
	prov := &cellProvider{}
	out, err := runner.Run(context.Background(), runner.Options{
		Manifest:  m,
		RunsDir:   t.TempDir(),
		Params:    map[string]string{"image": "chosen-base:9"},
		Scope:     &runner.Scope{Axes: map[string][]string{"from": {"1.0", "2.0"}}, Source: "cli"},
		Providers: map[string]target.Provider{"fake": prov},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, key := range []string{"1.0", "2.0"} {
		cell, readErr := record.Read(filepath.Join(out.Dir, "cells", key))
		if readErr != nil {
			t.Fatalf("read cell: %v", readErr)
		}
		started := only[record.RunStarted](t, cell, record.KindRunStarted)
		if started.Params["image"] != "chosen-base:9" {
			t.Errorf("cell %s ran with image %q, want the override", key, started.Params["image"])
		}
		if started.Params["from"] != key {
			t.Errorf("cell %s ran with from %q", key, started.Params["from"])
		}
	}
}

func TestAParameterThatFightsTheScopeIsRefused(t *testing.T) {
	t.Parallel()

	// Pinning an axis to one value while sweeping it is two instructions that
	// cannot both hold. Picking one silently would run a census the caller did
	// not ask for.
	_, err := runner.Run(context.Background(), runner.Options{
		Manifest:  manifestOf(t, sweptWithAParam),
		RunsDir:   t.TempDir(),
		Params:    map[string]string{"from": "1.0"},
		Scope:     &runner.Scope{Axes: map[string][]string{"from": {"1.0", "2.0"}}, Source: "cli"},
		Providers: map[string]target.Provider{"fake": &cellProvider{}},
	})
	if err == nil {
		t.Fatal("a param pinning a swept axis was accepted")
	}
	if !strings.Contains(err.Error(), "from") {
		t.Errorf("error %q does not name the axis", err)
	}
}

func TestAStoppedCensusAccountsForTheCellsItNeverReached(t *testing.T) {
	t.Parallel()

	// A census that stopped must not read as one that covered a smaller scope.
	// The cells nobody got to are counted, so the counts add up to what was
	// asked for.
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	out, rec, _, err := sweepRun(t, census{
		ctx:    ctx,
		values: []string{"1.0", "2.0", "3.0", "4.0"},
		perCell: func(string) (target.Result, error) {
			once.Do(cancel)
			return target.Result{RC: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	finished := only[record.RunFinished](t, rec, record.KindRunFinished)
	total := 0
	for _, n := range finished.Cells {
		total += n
	}
	if total != out.Total {
		t.Errorf("cells account for %d of %d; a census that stopped is still a census over its whole scope",
			total, out.Total)
	}
	if finished.Cells[record.StatusSkipped] == 0 {
		t.Errorf("cells = %v, want the ones nobody reached counted", finished.Cells)
	}
	if out.Status != record.StatusInterrupted {
		t.Errorf("status = %q, want %q", out.Status, record.StatusInterrupted)
	}
}

func TestNoCountOfNothingIsWrittenIntoTheEvidence(t *testing.T) {
	t.Parallel()

	// An explicit nought reads as a reading, and it is an absence.
	_, rec, _, err := sweepRun(t, census{
		values:  []string{"1.0"},
		perCell: func(string) (target.Result, error) { return target.Result{}, errors.New("no boot") },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	finished := only[record.RunFinished](t, rec, record.KindRunFinished)
	for status, n := range finished.Cells {
		if n == 0 {
			t.Errorf("cells carry %q: 0", status)
		}
	}
}
