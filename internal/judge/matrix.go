package judge

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/jmurray2011/testease/internal/record"
)

// Matrix reads a census: the parent record's scheduling, and each cell's own
// evidence underneath it.
//
// What it does NOT do is invent a vocabulary. A sweep's interesting answers -
// refused, upgraded, worse - are things a particular question means by them,
// and a general-purpose tool that hardcoded one product's words would be
// answering a question nobody asked it. So this judge does the part that is
// genuinely general: it applies the per-cell judge to every cell that measured,
// rolls the results up, and leaves the rest alone. A census whose cells need a
// domain conclusion gets a judge that knows that domain.
type Matrix struct{}

var _ Judge = Matrix{}

// Name is what a finding cites.
func (Matrix) Name() string { return "matrix" }

// Version changes whenever the conclusion this judge would reach changes -
// including when the judge it rolls up reaches a different one. A cell's
// verdict here is expect's verdict, so this number moves with expect's.
func (Matrix) Version() string { return "3" }

// Judge reads every cell of a census.
func (m Matrix) Judge(rec *record.Record) (Result, error) {
	keys, statuses, err := cells(rec)
	if err != nil {
		return Result{}, err
	}
	if len(keys) == 0 {
		return Result{}, &NothingToJudgeError{Judge: m.Name()}
	}

	result := Result{
		Verdict:      Pass,
		Cells:        map[string]StepVerdict{},
		Unjudged:     map[string]string{},
		CellEvidence: map[string]string{},
	}

	for _, key := range keys {
		status := statuses[key]
		if status != record.StatusMeasured {
			// A judge never manufactures a verdict for unmeasured evidence,
			// and a census that closes over cells nobody measured has to say
			// which ones rather than averaging them away.
			result.Unjudged[key] = string(status)
			continue
		}

		cell, err := record.Read(filepath.Join(rec.Dir, record.CellsDir, key))
		if err != nil {
			result.Unjudged[key] = "the cell's record could not be read: " + err.Error()
			continue
		}
		if events, err := os.ReadFile(filepath.Join(cell.Dir, record.EventsFile)); err == nil {
			result.CellEvidence[key] = HashBytes(events)
		}

		inner, err := (Expect{}).Judge(cell)
		if err != nil {
			// A cell with nothing to judge is not a cell that passed.
			result.Unjudged[key] = err.Error()
			continue
		}
		result.Cells[key] = StepVerdict{Verdict: inner.Verdict, Because: reasons(inner)}
	}

	if len(result.Cells) == 0 {
		return Result{}, &NothingToJudgeError{Judge: m.Name(), Unjudged: result.Unjudged}
	}
	for _, cell := range result.Cells {
		switch cell.Verdict {
		case Regressed:
			result.Verdict = Regressed
		case Unverified:
			if result.Verdict == Pass {
				result.Verdict = Unverified
			}
		}
	}
	if len(result.Unjudged) == 0 {
		result.Unjudged = nil
	}
	return result, nil
}

// reasons flattens a cell's per-step detail into the lines a census reader
// wants beside the cell: which step, and what did not hold.
func reasons(inner Result) []string {
	labels := make([]string, 0, len(inner.Steps))
	for label := range inner.Steps {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	var out []string
	for _, label := range labels {
		step := inner.Steps[label]
		if step.Verdict == Met {
			continue
		}
		for _, because := range step.Because {
			out = append(out, label+": "+because)
		}
	}
	return out
}

// cells is the census's cells in the order they were scheduled, and what the
// scheduler last said about each. A cell that started and never finished has
// no status, and not finishing is itself the answer.
func cells(rec *record.Record) ([]string, map[string]record.Status, error) {
	var keys []string
	statuses := map[string]record.Status{}

	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindCellStarted:
			started, err := record.Decode[record.CellStarted](e)
			if err != nil {
				return nil, nil, err
			}
			if _, seen := statuses[started.Key]; !seen {
				statuses[started.Key] = record.StatusUnfinished
				keys = append(keys, started.Key)
			}
		case record.KindCellFinished:
			finished, err := record.Decode[record.CellFinished](e)
			if err != nil {
				return nil, nil, err
			}
			if _, seen := statuses[finished.Key]; !seen {
				keys = append(keys, finished.Key)
			}
			statuses[finished.Key] = finished.Status
		}
	}
	return keys, statuses, nil
}
