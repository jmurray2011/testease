package judge

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/record"
)

const (
	ReproduceName    = "reproduce"
	ReproduceVersion = "2"
	ComparisonsDir   = "comparisons"
)

// ApplyReproduce reaches one conclusion across independent immutable records.
// It is kept beside the other judges because laying facts side by side is a
// rendering, while saying they agree is unambiguously a verdict.
func ApplyReproduce(recs []*record.Record) (Verdict, error) {
	if len(recs) < 2 {
		return Verdict{}, &NothingToJudgeError{Judge: ReproduceName, Unjudged: map[string]string{
			"records": "repeatability needs at least two independent records",
		}}
	}

	views := make([]reproductionRecord, 0, len(recs))
	seen := map[string]bool{}
	for _, rec := range recs {
		view, err := readReproductionRecord(rec)
		if err != nil {
			return Verdict{}, err
		}
		if seen[view.id] {
			return Verdict{}, fmt.Errorf("reproduce: record %s was given twice", view.id)
		}
		seen[view.id] = true
		views = append(views, view)
	}

	facts := slices.Clone(views[0].stable)
	sort.Strings(facts)
	if len(facts) == 0 {
		return Verdict{}, &NothingToJudgeError{Judge: ReproduceName, Unjudged: map[string]string{
			"stable_facts": "the records declare no stable facts",
		}}
	}

	result := Result{
		Verdict:        Pass,
		Steps:          map[string]StepVerdict{},
		RecordEvidence: map[string]string{},
	}
	for _, view := range views {
		for name, hash := range view.evidence {
			result.RecordEvidence[name] = hash
		}
	}

	var contextReasons []string
	for _, view := range views {
		if view.scenarioHash != views[0].scenarioHash {
			contextReasons = append(contextReasons, fmt.Sprintf("%s has scenario %s, not %s", view.id, view.scenarioHash, views[0].scenarioHash))
		}
		if !slices.Equal(sortedStrings(view.stable), facts) {
			contextReasons = append(contextReasons, fmt.Sprintf("%s declares a different stable-fact set", view.id))
		}
		if !maps.Equal(view.inputs, views[0].inputs) {
			contextReasons = append(contextReasons, fmt.Sprintf("%s consumed different input identities", view.id))
		}
		if view.status != record.StatusMeasured {
			contextReasons = append(contextReasons, fmt.Sprintf("%s closed %s, not measured", view.id, view.status))
		}
		if len(view.repairs) > 0 {
			contextReasons = append(contextReasons, fmt.Sprintf("%s was read with %d repair report(s)", view.id, len(view.repairs)))
		}
		contextReasons = append(contextReasons, view.context...)
	}
	if len(contextReasons) > 0 {
		result.Verdict = Unverified
		result.Steps["context"] = StepVerdict{Verdict: Unverifiable, Because: contextReasons}
		// Values from different questions or different inputs can be displayed,
		// but they cannot settle repeatability. Stop before a measured difference
		// is accidentally promoted into a regression about incomparable runs.
		return applyManyEnvelope(result), nil
	}

	for _, key := range facts {
		values := map[string][]string{}
		var missing []string
		for _, view := range views {
			value, ok := view.facts[key]
			if !ok {
				missing = append(missing, view.id)
				continue
			}
			values[value] = append(values[value], view.id)
		}

		if len(missing) > 0 {
			result.Steps[key] = StepVerdict{
				Verdict: Unverifiable,
				Because: []string{fmt.Sprintf("no measured reading in %s", strings.Join(missing, ", "))},
			}
			if result.Verdict == Pass {
				result.Verdict = Unverified
			}
			continue
		}

		var because []string
		for _, value := range slices.Sorted(maps.Keys(values)) {
			because = append(because, fmt.Sprintf("%q in %s", value, strings.Join(values[value], ", ")))
		}
		if len(values) == 1 {
			result.Steps[key] = StepVerdict{Verdict: Met, Because: because}
		} else {
			result.Steps[key] = StepVerdict{Verdict: Unmet, Because: append([]string{"declared stable, and the readings differ"}, because...)}
			result.Verdict = Regressed
		}
	}

	return applyManyEnvelope(result), nil
}

type reproductionRecord struct {
	id           string
	hash         string
	evidence     map[string]string
	scenarioHash string
	stable       []string
	inputs       map[string]string
	facts        map[string]string
	status       record.Status
	repairs      []record.Report
	context      []string
}

func readReproductionRecord(rec *record.Record) (reproductionRecord, error) {
	view, err := readSingleReproductionRecord(rec)
	if err != nil {
		return view, err
	}

	keys, statuses, err := cells(rec)
	if err != nil {
		return view, err
	}
	if len(keys) == 0 {
		return view, nil
	}

	// A census records its readings in child records. Qualifying each declared
	// stable fact by cell keeps unlike matrix rows apart while still letting two
	// complete censuses answer one reproducibility question. The evidence map
	// pins every child event stream the conclusion reads, not merely the parent
	// scheduler record.
	declared := slices.Clone(view.stable)
	view.stable = nil
	view.facts = map[string]string{}
	for _, key := range keys {
		cell, err := record.Read(filepath.Join(rec.Dir, record.CellsDir, key))
		if err != nil {
			return view, fmt.Errorf("reproduce: read cell %s in %s: %w", key, view.id, err)
		}
		child, err := readSingleReproductionRecord(cell)
		if err != nil {
			return view, err
		}
		view.evidence[view.id+"/"+record.CellsDir+"/"+key] = child.hash

		for _, fact := range declared {
			qualified := key + "/" + fact
			view.stable = append(view.stable, qualified)
			if value, ok := child.facts[fact]; ok {
				view.facts[qualified] = value
			}
		}
		for name, hash := range child.inputs {
			view.inputs[key+"/"+name] = hash
		}

		if statuses[key] != record.StatusMeasured {
			view.context = append(view.context, fmt.Sprintf("%s cell %s closed %s, not measured", view.id, key, statuses[key]))
		}
		if child.status != statuses[key] {
			view.context = append(view.context, fmt.Sprintf("%s cell %s record closed %s, but its parent recorded %s", view.id, key, child.status, statuses[key]))
		}
		if child.scenarioHash != view.scenarioHash {
			view.context = append(view.context, fmt.Sprintf("%s cell %s has scenario %s, not %s", view.id, key, child.scenarioHash, view.scenarioHash))
		}
		if !slices.Equal(sortedStrings(child.stable), sortedStrings(declared)) {
			view.context = append(view.context, fmt.Sprintf("%s cell %s declares a different stable-fact set", view.id, key))
		}
		view.repairs = append(view.repairs, child.repairs...)
	}
	return view, nil
}

func readSingleReproductionRecord(rec *record.Record) (reproductionRecord, error) {
	latest := rec.Latest()
	view := reproductionRecord{
		id: filepath.Base(rec.Dir), inputs: map[string]string{}, facts: map[string]string{}, repairs: rec.Reports,
	}
	body, err := os.ReadFile(filepath.Join(rec.Dir, record.EventsFile))
	if err != nil {
		return view, fmt.Errorf("hash %s: %w", view.id, err)
	}
	view.hash = HashBytes(body)

	measured := map[int]bool{}
	taken := map[int][]record.Fact{}
	for _, event := range latest.Events {
		switch event.Kind {
		case record.KindRunStarted:
			started, err := record.Decode[record.RunStarted](event)
			if err != nil {
				return view, err
			}
			if started.RunID != "" {
				view.id = started.RunID
			}
			view.scenarioHash = started.Scenario.Hash
			if started.Reproduce != nil {
				view.stable = slices.Clone(started.Reproduce.Facts)
			}
		case record.KindInputResolved:
			input, err := record.Decode[record.InputResolved](event)
			if err != nil {
				return view, err
			}
			view.inputs[input.Name] = input.SHA256
		case record.KindOperation:
			op, err := record.Decode[record.Operation](event)
			if err != nil {
				return view, err
			}
			measured[event.Step] = op.Status == record.StatusMeasured
		case record.KindFact:
			fact, err := record.Decode[record.Fact](event)
			if err != nil {
				return view, err
			}
			taken[event.Step] = append(taken[event.Step], fact)
		case record.KindRunFinished:
			finished, err := record.Decode[record.RunFinished](event)
			if err != nil {
				return view, err
			}
			view.status = finished.Status
		}
	}
	for step, facts := range taken {
		if !measured[step] {
			continue
		}
		for _, fact := range facts {
			if fact.Value == nil {
				delete(view.facts, fact.Key)
				continue
			}
			view.facts[fact.Key] = *fact.Value
		}
	}
	view.evidence = map[string]string{view.id: view.hash}
	return view, nil
}

func sortedStrings(values []string) []string {
	out := slices.Clone(values)
	sort.Strings(out)
	return out
}

func applyManyEnvelope(result Result) Verdict {
	return Verdict{
		Schema: Schema, Judge: ReproduceName, Version: ReproduceVersion,
		JudgedAt: timeNow(),
		Evidence: Evidence{Records: result.RecordEvidence},
		Verdict:  result.Verdict, Steps: result.Steps, Unjudged: result.Unjudged,
	}
}

// timeNow is a seam only for keeping the multi-record envelope shaped exactly
// like Apply's without putting a clock inside the conclusion.
var timeNow = func() time.Time { return time.Now().UTC() }

// SaveComparison writes a multi-record verdict outside every source record.
// The id is derived from its sorted evidence pins, so argument order cannot
// create two names for the same comparison.
func SaveComparison(runsDir string, verdict Verdict) (string, string, Outcome, error) {
	id, err := ComparisonID(verdict)
	if err != nil {
		return "", "", Written, err
	}
	dir := filepath.Join(runsDir, ComparisonsDir, id)
	outcome, err := Save(dir, verdict)
	return id, filepath.Join(dir, VerdictsDir, verdict.Judge+"@"+verdict.Version+".json"), outcome, err
}

// ComparisonID is the content-derived name SaveComparison gives a verdict.
// Exposed so an orchestrator can cite the comparison it caused without
// scraping human terminal output.
func ComparisonID(verdict Verdict) (string, error) {
	pins, err := json.Marshal(verdict.Evidence.Records)
	if err != nil {
		return "", err
	}
	id := strings.TrimPrefix(HashBytes(pins), "sha256:")[:16]
	return id, nil
}
