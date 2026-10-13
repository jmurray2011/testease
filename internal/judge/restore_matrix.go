package judge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jmurray2011/testease/internal/record"
)

// Restore-matrix's vocabulary is deliberately about the product operation,
// not merely about whether the test's preconditions held. A team reader can
// therefore distinguish "the restore hung" from "the experiment could not
// establish an answer" without interpreting raw facts or an exit code.
const (
	Restored          = "restored"
	RestoreFailed     = "restore-failed"
	RestoreHang       = "hang"
	SourceDataMissing = "source-data-missing"
	TargetNotReplaced = "target-not-replaced"
)

// RestoreMatrix answers a restore census's product question from primitive
// readings. It deliberately does not read restore.outcome: that is
// the probe's own interpretation of its readings, and trusting it would make
// the verdict impossible to derive independently from the record.
type RestoreMatrix struct{}

var _ Judge = RestoreMatrix{}

func (RestoreMatrix) Name() string    { return "restore-matrix" }
func (RestoreMatrix) Version() string { return "1" }

// Judge reads every measured cell in a restore census.
func (m RestoreMatrix) Judge(rec *record.Record) (Result, error) {
	keys, statuses, err := cells(rec)
	if err != nil {
		return Result{}, err
	}
	if len(keys) == 0 {
		return Result{}, &NothingToJudgeError{Judge: m.Name()}
	}

	result := Result{
		Verdict:      Restored,
		Cells:        map[string]StepVerdict{},
		Unjudged:     map[string]string{},
		CellEvidence: map[string]string{},
	}

	for _, key := range keys {
		if status := statuses[key]; status != record.StatusMeasured {
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

		result.Cells[key] = judgeRestoreCell(cell)
	}

	if len(result.Cells) == 0 {
		return Result{}, &NothingToJudgeError{Judge: m.Name(), Unjudged: result.Unjudged}
	}
	for _, cell := range result.Cells {
		switch cell.Verdict {
		case RestoreHang, SourceDataMissing, TargetNotReplaced:
			result.Verdict = RestoreFailed
		case Unverified:
			if result.Verdict == Restored {
				result.Verdict = Unverified
			}
		}
	}
	if len(result.Unjudged) > 0 && result.Verdict == Restored {
		result.Verdict = Unverified
	}
	if len(result.Unjudged) == 0 {
		result.Unjudged = nil
	}
	return result, nil
}

// judgeRestoreCell first proves that the declared experimental preconditions
// held, then derives the product outcome from lower-level facts. A failed or
// unreadable control makes the experiment unverified, however suggestive the
// later readings may be.
func judgeRestoreCell(rec *record.Record) StepVerdict {
	premises, err := (Expect{}).Judge(rec)
	if err != nil {
		return StepVerdict{Verdict: Unverified, Because: []string{"the experiment's preconditions could not be checked: " + err.Error()}}
	}
	if premises.Verdict != Pass {
		why := reasons(premises)
		if len(why) == 0 {
			why = []string{"the experiment's declared preconditions did not all hold"}
		}
		return StepVerdict{Verdict: Unverified, Because: why}
	}

	facts, conflicts, err := restoreFacts(rec.Latest())
	if err != nil {
		return StepVerdict{Verdict: Unverified, Because: []string{"the restore readings could not be decoded: " + err.Error()}}
	}
	if len(conflicts) > 0 {
		return StepVerdict{Verdict: Unverified, Because: conflicts}
	}

	timedOut, ok := factReading(facts, "restore.timed_out")
	if !ok {
		return missingRestoreReading("restore.timed_out", facts)
	}
	switch timedOut {
	case "yes":
		return StepVerdict{Verdict: RestoreHang, Because: []string{"the restore exceeded its recorded time limit"}}
	case "no":
	default:
		return StepVerdict{Verdict: Unverified, Because: []string{fmt.Sprintf("restore.timed_out: expected %q or %q, observed %q", "yes", "no", timedOut)}}
	}

	source, ok := factReading(facts, "source.version")
	if !ok {
		return missingRestoreReading("source.version", facts)
	}
	marker, ok := factReading(facts, "marker.rung")
	if !ok {
		return missingRestoreReading("marker.rung", facts)
	}
	if marker != source {
		return StepVerdict{
			Verdict: SourceDataMissing,
			Because: []string{fmt.Sprintf("the restored marker is %q; the source fixture is %q", marker, source)},
		}
	}

	sentinel, ok := factReading(facts, "sentinel.count")
	if !ok {
		return missingRestoreReading("sentinel.count", facts)
	}
	if sentinel != "0" {
		return StepVerdict{
			Verdict: TargetNotReplaced,
			Because: []string{fmt.Sprintf("the target-only sentinel remained after restore (count %q)", sentinel)},
		}
	}

	return StepVerdict{
		Verdict: Restored,
		Because: []string{
			fmt.Sprintf("the source marker %q arrived", source),
			"the target-only sentinel was removed",
		},
	}
}

// restoreFacts gathers the last structured reading of each key from measured
// steps. If two measured steps claim the same key, the evidence is ambiguous:
// silently choosing one would let event order change a product conclusion.
func restoreFacts(rec *record.Record) (map[string]record.Fact, []string, error) {
	steps, order, err := gather(rec)
	if err != nil {
		return nil, nil, err
	}

	facts := map[string]record.Fact{}
	owners := map[string]string{}
	var conflicts []string
	for _, n := range order {
		step := steps[n]
		if step.status != record.StatusMeasured {
			continue
		}
		for key, fact := range step.facts {
			if owner, exists := owners[key]; exists && owner != step.label {
				conflicts = append(conflicts, fmt.Sprintf("%s was read by both %q and %q", key, owner, step.label))
				continue
			}
			facts[key] = fact
			owners[key] = step.label
		}
	}
	sort.Strings(conflicts)
	return facts, conflicts, nil
}

func factReading(facts map[string]record.Fact, key string) (string, bool) {
	fact, exists := facts[key]
	if !exists || fact.Value == nil {
		return "", false
	}
	return *fact.Value, true
}

func missingRestoreReading(key string, facts map[string]record.Fact) StepVerdict {
	why := "the record contains no reading"
	if fact, exists := facts[key]; exists && fact.Error != "" {
		why = fact.Error
	}
	return StepVerdict{Verdict: Unverified, Because: []string{key + ": " + why}}
}
