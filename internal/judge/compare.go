package judge

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/jmurray2011/testease/internal/record"
)

// Compare answers the one question an expectation cannot: whether a reading
// taken in more than one place agrees with itself.
//
// The expect judge refuses this deliberately - an expectation is about the step
// that declared it, and a judge reaching across steps with the manifest as its
// input language is the rule engine the non-goals forbid. So this is a judge
// that says out loud that it is asking, and it names its subjects rather than
// discovering them: a comparison nobody declared is a conclusion nobody asked
// for.
//
// It exists for a shape the corpus keeps meeting. "Are these credentials shipped
// fixed or generated per install" cannot be answered by reading one machine,
// however carefully - only by two independent installs from the same media,
// compared. And the run is only falsifiable if the same record also carries a
// value that MUST differ: without that, "identical" is indistinguishable from a
// harness that reads the same host twice.
type Compare struct {
	// Same and Differ name fact keys. Every reading of a named key anywhere in
	// the record is gathered and the whole set is judged at once, because
	// "these two agree and that third one does not" is not a pass.
	Same   []string
	Differ []string
}

var _ Judge = Compare{}

// Name is what a finding cites.
func (Compare) Name() string { return "compare" }

// Version changes whenever the conclusion this judge would reach changes.
func (Compare) Version() string { return "1" }

// ErrNoSubjects is a compare that was asked to compare nothing. It is an error
// rather than a pass: a judge with no subjects would otherwise report success
// over evidence it never looked at, which is the exact shape of a control that
// cannot fail.
var ErrNoSubjects = errors.New("compare names no keys: a judge with no subjects would pass by looking at nothing")

// Judge reads the record and reaches a verdict.
func (c Compare) Judge(rec *record.Record) (Result, error) {
	if len(c.Same) == 0 && len(c.Differ) == 0 {
		return Result{}, ErrNoSubjects
	}

	readings := gatherReadings(rec.Latest())
	result := Result{
		Verdict:  Pass,
		Steps:    map[string]StepVerdict{},
		Unjudged: map[string]string{},
	}

	for _, key := range slices.Sorted(slices.Values(c.Same)) {
		result.Steps[key] = c.check(key, readings[key], true)
	}
	for _, key := range slices.Sorted(slices.Values(c.Differ)) {
		// A key declared both ways is a manifest that contradicts itself, and
		// the later declaration silently winning is how that goes unnoticed.
		if _, both := result.Steps[key]; both {
			result.Steps[key] = StepVerdict{
				Verdict: Unverifiable,
				Because: []string{key + ": declared as both same and differ, so no reading of it can settle anything"},
			}
			continue
		}
		result.Steps[key] = c.check(key, readings[key], false)
	}

	// A real disagreement outranks one nobody could check: a measured conflict
	// is not softened by a missing reading elsewhere in the run.
	for _, v := range result.Steps {
		switch v.Verdict {
		case Unmet:
			result.Verdict = Regressed
		case Unverifiable:
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

// reading is one value of one key, and where it was read.
type reading struct {
	label string
	value string
}

func (c Compare) check(key string, taken []reading, same bool) StepVerdict {
	if len(taken) < 2 {
		// One reading is not a comparison, and no reading is not evidence of
		// agreement. Calling either a pass would be a conclusion with no
		// measurement behind it.
		return StepVerdict{
			Verdict: Unverifiable,
			Because: []string{fmt.Sprintf("%s: %d measured reading(s), and a comparison needs two", key, len(taken))},
		}
	}

	distinct := map[string][]string{}
	for _, r := range taken {
		distinct[r.value] = append(distinct[r.value], r.label)
	}

	// Values are quoted on both sides for the same reason expect quotes them: a
	// trailing CR is a real difference, and a reason that renders it invisible
	// says two identical-looking things disagreed and lies about why.
	var where []string
	for _, v := range slices.Sorted(maps.Keys(distinct)) {
		where = append(where, fmt.Sprintf("%q on %v", v, distinct[v]))
	}

	switch {
	case same && len(distinct) > 1:
		return StepVerdict{Verdict: Unmet, Because: append([]string{key + ": declared identical everywhere, and it is not"}, where...)}
	case !same && len(distinct) < len(taken):
		return StepVerdict{Verdict: Unmet, Because: append([]string{key + ": declared different everywhere, and two readings match"}, where...)}
	default:
		return StepVerdict{Verdict: Met, Because: where}
	}
}

// gatherReadings collects every fact reading in the record, keyed by fact key.
//
// Only measured steps contribute. A step whose probe died left a value behind
// that is not a reading of the machine, and comparing it against a real one
// would manufacture a disagreement out of infrastructure weather.
func gatherReadings(rec *record.Record) map[string][]reading {
	labels := map[int]string{}
	status := map[int]record.Status{}
	facts := map[int][]record.Fact{}

	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindStepStarted:
			started, err := record.Decode[record.StepStarted](e)
			if err != nil {
				continue
			}
			labels[e.Step] = started.Label

		case record.KindOperation:
			op, err := record.Decode[record.Operation](e)
			if err != nil {
				continue
			}
			status[e.Step] = op.Status

		case record.KindFact:
			fact, err := record.Decode[record.Fact](e)
			if err != nil {
				continue
			}
			facts[e.Step] = append(facts[e.Step], fact)
		}
	}

	out := map[string][]reading{}
	for step, taken := range facts {
		if status[step] != record.StatusMeasured {
			continue
		}
		// The last reading of a key is the one the probe stands behind, the way
		// the shell's `tail -1` did. Both stay in the events.
		last := map[string]record.Fact{}
		for _, f := range taken {
			last[f.Key] = f
		}
		for key, f := range last {
			if f.Value == nil {
				continue
			}
			out[key] = append(out[key], reading{label: labels[step], value: *f.Value})
		}
	}
	for key := range out {
		sort.Slice(out[key], func(i, j int) bool { return out[key][i].label < out[key][j].label })
	}
	return out
}
