package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/scenario"
)

// runCensus sweeps a scope, one sub-record per cell.
//
// The parent record carries the run's lifecycle and the scheduling; each cell
// is a self-contained sub-record with its own writer. That split is not
// tidiness: parallel cells are parallel writers, and interleaving them into one
// file would need locking and would produce a stream no human can read in
// order.
func runCensus(ctx context.Context, opt Options) (*Outcome, error) {
	if opt.Manifest == nil {
		return nil, errors.New("a census needs a scenario to resolve per cell")
	}
	started := time.Now()

	runID, dir, scope, done, err := censusScope(opt)
	if err != nil {
		return nil, err
	}

	cells, err := scope.Cells()
	if err != nil {
		return nil, err
	}
	remaining := Remaining(cells, done)

	for name := range opt.Params {
		if _, swept := scope.Axes[name]; swept {
			return nil, fmt.Errorf(
				"param %q is pinned to one value and swept as an axis at the same time; "+
					"a census cannot honour both", name,
			)
		}
	}

	w, err := record.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open record: %w", err)
	}
	defer func() { _ = w.Close() }()

	// The census tags its own lines with the run; runOne tags a cell's lines
	// with the run and the cell. Tagging opt.Log here as well would put the
	// key on every cell line twice.
	c := &census{
		log:    opt.Log.With("run", runID),
		opt:    opt,
		w:      w,
		dir:    dir,
		runID:  runID,
		counts: map[record.Status]int{},
		outcome: &Outcome{
			RunID: runID,
			Dir:   dir,
			Total: len(cells),
		},
	}

	// Cells carried over from an earlier pass are measured, and the census
	// still covers them - a resumed record is the union of its passes, and
	// saying so is the difference between a census that covered the set and
	// one that looks like it did. A count of zero is not written: an explicit
	// nought in the evidence reads as a reading, and it is an absence.
	if len(done) > 0 {
		c.counts[record.StatusMeasured] = len(done)
	}

	c.emit(record.KindRunStarted, record.RunStarted{
		RunID:       runID,
		Scenario:    scenarioRef(opt.Manifest.Name, opt.Manifest.ContentIdentity),
		Judge:       declaredManifestJudge(opt.Manifest),
		Reproduce:   declaredManifestReproduce(opt.Manifest),
		Views:       declaredManifestViews(opt.Manifest),
		SecretFiles: slices.Sorted(maps.Keys(opt.Manifest.Secrets)),
		Tool:        opt.Tool,
		Driver:      record.DriverManifest,
		Params:      opt.Params,
		Matrix: &record.Matrix{
			Axes:         scope.Axes,
			Exclude:      scope.Exclude,
			Source:       scope.Source,
			DiscoveredOn: scope.DiscoveredOn,
			ResumeOf:     opt.Resume,
		},
		Host:           hostRef(),
		EnvFingerprint: measurementFingerprint(opt.Measurement),
	})
	for _, name := range slices.Sorted(maps.Keys(opt.Inputs)) {
		c.emit(record.KindInputResolved, recordedInput(opt.Inputs[name]))
	}

	c.log.Info("census starting",
		"cells", len(cells), "remaining", len(remaining), "carried over", len(done), "parallel", parallelism(opt))

	skipped := c.sweep(ctx, remaining)
	c.close(started, skipped)
	return c.outcome, nil
}

func declaredManifestReproduce(manifest *scenario.Manifest) *record.ReproduceRef {
	if manifest.Reproduce == nil {
		return nil
	}
	return &record.ReproduceRef{Facts: slices.Clone(manifest.Reproduce.Facts)}
}

func declaredManifestViews(manifest *scenario.Manifest) map[string]record.ViewRef {
	if len(manifest.Views) == 0 {
		return nil
	}
	out := make(map[string]record.ViewRef, len(manifest.Views))
	for name, view := range manifest.Views {
		out[name] = record.ViewRef{Kind: view.Kind, Facts: slices.Clone(view.Facts)}
	}
	return out
}

// censusScope works out what this run covers and what is already done.
//
// A resumed census takes its scope from what the first pass wrote down, not
// from whatever discovery would find today: re-discovering would make what a
// record claims depend on the machine that resumed it.
func censusScope(opt Options) (runID, dir string, scope Scope, done map[string]bool, err error) {
	if opt.Resume == "" {
		runID = opt.RunID
		if runID == "" {
			runID = record.RunID(opt.Manifest.Name, time.Now())
		}
		return runID, filepath.Join(opt.RunsDir, runID), *opt.Scope, nil, nil
	}

	runID = opt.Resume
	dir = filepath.Join(opt.RunsDir, runID)

	prior, err := record.Read(dir)
	if err != nil {
		return "", "", Scope{}, nil, fmt.Errorf("resume %s: %w", runID, err)
	}
	scope, err = RecordedScope(prior)
	if err != nil {
		return "", "", Scope{}, nil, err
	}
	return runID, dir, scope, Measured(prior), nil
}

type census struct {
	log   *slog.Logger
	opt   Options
	w     *record.Writer
	dir   string
	runID string

	mu      sync.Mutex
	counts  map[record.Status]int
	outcome *Outcome
}

// sweep runs the remaining cells, bounded. It returns the cells it never got
// to, so that a census which stopped early still accounts for its whole scope
// rather than reading as though it covered a smaller one.
func (c *census) sweep(ctx context.Context, remaining []Cell) []Cell {
	slots := make(chan struct{}, parallelism(c.opt))
	var wg sync.WaitGroup
	var unscheduled []Cell

	for i, cell := range remaining {
		if ctx.Err() != nil {
			// Stop scheduling new cells. What is in flight finishes, and the
			// record closes saying it was cut short.
			unscheduled = remaining[i:]
			break
		}
		slots <- struct{}{}
		wg.Add(1)

		go func(cell Cell) {
			defer wg.Done()
			defer func() { <-slots }()
			c.runCell(ctx, cell)
		}(cell)
	}

	wg.Wait()
	return unscheduled
}

func (c *census) runCell(ctx context.Context, cell Cell) {
	c.emit(record.KindCellStarted, record.CellStarted{Key: cell.Key})

	status := record.StatusInfraFail
	plan, err := c.opt.Manifest.Resolve(cellParams(c.opt.Params, cell))
	if err != nil {
		// A cell whose scenario would not resolve never reached a machine.
		// That is still a cell of this census and it is still accounted for.
		c.log.Warn("cell could not be resolved", "cell", cell.Key, "error", err)
	} else {
		outcome, runErr := runOne(ctx, c.opt, plan,
			filepath.Join(c.dir, record.CellsDir, cell.Key), c.runID, cell.Key)
		switch {
		case outcome != nil:
			status = outcome.Status
		case runErr != nil:
			c.log.Warn("cell left no record", "cell", cell.Key, "error", runErr)
		}
		if runErr != nil {
			c.log.Warn("cell did not measure cleanly", "cell", cell.Key, "status", status, "error", runErr)
		}
	}

	c.mu.Lock()
	c.counts[status]++
	c.mu.Unlock()

	c.emit(record.KindCellFinished, record.CellFinished{Key: cell.Key, Status: status})
}

func (c *census) close(started time.Time, unscheduled []Cell) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A cell nobody reached is still a cell of this census. Counting it says
	// so, and the counts then add up to the scope instead of quietly
	// describing a smaller census than the one that was asked for.
	if len(unscheduled) > 0 {
		c.counts[record.StatusSkipped] = len(unscheduled)
	}

	measured := c.counts[record.StatusMeasured]
	c.outcome.Measured = measured
	c.outcome.Cells = c.counts

	status := worstOf(c.counts)
	if len(unscheduled) > 0 {
		status = record.Worse(status, record.StatusInterrupted)
	}
	c.outcome.Status = status

	if measured < c.outcome.Total {
		// Loudly. A census that closes over cells nobody measured must never
		// read as one that covered them.
		c.log.Warn("census closed with unmeasured cells",
			"measured", measured, "cells", c.outcome.Total, "by status", c.counts)
	}

	c.emitLocked(record.KindRunFinished, record.RunFinished{
		Status:    status,
		Cells:     c.counts,
		DurationS: time.Since(started).Seconds(),
	})

	if rec, err := record.Read(c.dir); err == nil {
		if err := record.WriteSummary(c.dir, record.Summarize(rec)); err != nil {
			c.log.Warn("could not write the summary", "error", err)
		}
	}
}

// worstOf is the census's status when something went unmeasured: the worst any
// cell reached on the runner's axis. Nothing here is about whether the news was
// good.
func worstOf(counts map[record.Status]int) record.Status {
	worst := record.StatusMeasured
	for status, n := range counts {
		if n > 0 {
			worst = record.Worse(worst, status)
		}
	}
	return worst
}

func (c *census) emit(kind record.Kind, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emitLocked(kind, data)
}

// emitLocked writes to the parent's record. The parent file has exactly one
// writer - this scheduler - and cells never touch it.
func (c *census) emitLocked(kind record.Kind, data any) {
	if _, err := c.w.Emit(kind, 0, data); err != nil {
		c.log.Warn("could not record an event", "kind", kind, "error", err)
	} else if c.opt.Observe != nil {
		c.opt.Observe(ProgressEvent{
			RunID: c.runID, Kind: kind, Total: c.outcome.Total, Data: data,
		})
	}
}

// cellParams is what one cell resolves with: the run's parameters, plus the
// axis values that make this cell. The two cannot disagree - a param that
// names a swept axis is refused before any cell runs.
func cellParams(runParams map[string]string, cell Cell) map[string]string {
	params := make(map[string]string, len(runParams)+len(cell.Params))
	for k, v := range runParams {
		params[k] = v
	}
	for k, v := range cell.Params {
		params[k] = v
	}
	return params
}

func parallelism(opt Options) int {
	if opt.Parallel < 1 {
		return 1
	}
	return opt.Parallel
}

func declaredManifestJudge(m *scenario.Manifest) *record.JudgeRef {
	if m.Judge == nil {
		return nil
	}
	return &record.JudgeRef{Name: m.Judge.Name, Version: m.Judge.Version, Params: m.Judge.Params}
}
