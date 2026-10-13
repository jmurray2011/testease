package judge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// The verdicts expect reaches. Its pass set is pass, and nothing else.
//
// Unverified is separate from regressed on purpose. A control the judge had no
// reading for did not fail - nobody checked it - and saying "regressed" would
// be the judge stating a domain outcome it has no measurement behind. That is
// exactly the impersonation this design exists to prevent, and it is the
// difference between "the product broke" and "the instruments were not there".
const (
	Pass       = "pass"
	Regressed  = "regressed"
	Unverified = "unverified"

	Met          = "met"
	Unmet        = "unmet"
	Unverifiable = "unverifiable"
)

// Expect judges a run against the expectations its own steps declared.
//
// It is the successor to `$3 ~ /CONTROL negative/ && $6 != "no"`. That awk line
// read a verdict out of a rendered row, so a row that was not seven fields
// produced a wrong verdict from correct measurements. This reads the events.
//
// Two things it deliberately does not do. It does not read the runner's
// control-checked answer: the runner checks a control at run time for
// fail-fast visibility, and the judge is the authority, so the observation is
// re-derived from the operation, fingerprint and fact events every time. And it
// does not touch claims - a claim is testimony, and a judge that parses
// testimony has rebuilt the coupling this design exists to remove. Facts are
// the other thing entirely: the structured channel, read off the machine on
// fd 3, and reading them is what a judge is for.
//
// What it still does not do is compare one step's reading with another's. An
// expectation is about the step that declared it. "The service was active
// before and is not now" is a conclusion about a run rather than about a step,
// and a judge that reached across steps here would be a rule engine with the
// manifest as its input language - the DSL the non-goals forbid. That question
// belongs to a judge that says it is asking it.
type Expect struct{}

var _ Judge = Expect{}

// Name is what a finding cites.
func (Expect) Name() string { return "expect" }

// Version changes whenever the conclusion this judge would reach changes, so
// that an old finding still means what it meant.
//
// 2 reads facts; 3 quotes the values in its reasons, which changes the verdict
// file it writes for the same evidence. Two implementations disagreeing under
// one citation key is exactly what a version number is for, and a reason is
// part of the verdict rather than decoration on it.
func (Expect) Version() string { return "3" }

// what a step declared and what actually happened, gathered from the events.
type observed struct {
	label       string
	status      record.Status
	rc          int
	sawRC       bool
	fingerprint record.FingerprintResult
	facts       map[string]record.Fact
	expected    map[string]string
}

// Judge reads the record and reaches a verdict.
func (e Expect) Judge(rec *record.Record) (Result, error) {
	// The attempt that stands. A cell re-run on a resume keeps its failed
	// attempt in the same file, and judging a blend of the two would let a
	// step that failed and was re-run read as though the failure never
	// happened - with the failure still there, in the same file.
	steps, order, err := gather(rec.Latest())
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Verdict:  Pass,
		Steps:    map[string]StepVerdict{},
		Unjudged: map[string]string{},
	}

	for _, n := range order {
		step := steps[n]
		if len(step.expected) == 0 {
			// Nothing was declared, so there is nothing to be met or unmet.
			// Silence here is not a pass; it is an absence.
			continue
		}
		if step.status != record.StatusMeasured {
			// A judge never manufactures a verdict for unmeasured evidence.
			result.Unjudged[step.label] = string(step.status)
			continue
		}
		result.Steps[step.label] = check(step)
	}

	if len(result.Steps) == 0 {
		return Result{}, &NothingToJudgeError{Judge: e.Name(), Unjudged: result.Unjudged}
	}

	// An expectation that genuinely did not hold outranks one nobody could
	// check: a real regression is not softened by a missing instrument
	// elsewhere in the run.
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

// check compares one step's declarations against what the events say happened.
func check(step observed) StepVerdict {
	var unmet, unverifiable []string

	for _, field := range sorted(step.expected) {
		want := step.expected[field]
		switch field {
		case "rc":
			if !step.sawRC {
				unverifiable = append(unverifiable, "rc: expected "+want+", and the step recorded no exit code")
				continue
			}
			if got := fmt.Sprint(step.rc); got != want {
				unmet = append(unmet, "rc: expected "+want+", observed "+got)
			}

		case "fingerprint":
			got := step.fingerprint
			switch got {
			case "":
				unverifiable = append(unverifiable,
					"fingerprint: expected "+want+", and no fingerprint was taken")
			case record.FingerprintUnavailable, record.FingerprintErrored, record.FingerprintUnknown:
				// unavailable is the target having no instrument, errored is
				// the instrument failing, unknown is it saying something
				// nobody recognizes. None of the three is a reading of the
				// machine, so none of them can disagree with an expectation.
				unverifiable = append(unverifiable,
					"fingerprint: expected "+want+", observed "+string(got)+", which is not a reading of the machine")
			default:
				if string(got) != want {
					unmet = append(unmet, "fingerprint: expected "+want+", observed "+string(got))
				}
			}

		default:
			key, isFact := strings.CutPrefix(field, record.FactPrefix)
			if !isFact {
				// A declared check this judge cannot make must not pass
				// quietly: the scenario believes something is being checked.
				unverifiable = append(unverifiable, field+": expected "+want+
					", and expect judges rc, fingerprint and facts only")
				continue
			}

			fact, read := step.facts[key]
			switch {
			case !read:
				// A reading nobody took did not fail. Calling it unmet would
				// be a domain conclusion with no measurement behind it.
				unverifiable = append(unverifiable, field+": expected "+want+
					", and this step read no "+key)

			case fact.Value == nil:
				// A fact whose probe failed is evidence, and it is not a
				// reading of the machine, so it cannot disagree with anything.
				why := fact.Error
				if why == "" {
					why = "the probe recorded no value"
				}
				unverifiable = append(unverifiable, field+": expected "+want+
					", and the reading is missing: "+why)

			case *fact.Value != want:
				// Quoted, both sides. Values are compared verbatim, so a
				// trailing CR is a real difference - and a reason that renders
				// it invisible says "expected 9.0.106, observed 9.0.106" and
				// lies about why it disagreed.
				unmet = append(unmet, fmt.Sprintf("%s: expected %q, observed %q", field, want, *fact.Value))
			}
		}
	}

	switch {
	case len(unmet) > 0:
		return StepVerdict{Verdict: Unmet, Because: append(unmet, unverifiable...)}
	case len(unverifiable) > 0:
		return StepVerdict{Verdict: Unverifiable, Because: unverifiable}
	default:
		return StepVerdict{Verdict: Met}
	}
}

// gather walks the events once, collecting what each step declared and what it
// did. Order is the order the steps started in, so a verdict reads the way the
// run happened.
func gather(rec *record.Record) (map[int]observed, []int, error) {
	steps := map[int]observed{}
	var order []int

	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindStepStarted:
			started, err := record.Decode[record.StepStarted](e)
			if err != nil {
				return nil, nil, err
			}
			if _, seen := steps[e.Step]; !seen {
				order = append(order, e.Step)
			}
			step := steps[e.Step]
			step.label = started.Label
			steps[e.Step] = step

		case record.KindOperation:
			op, err := record.Decode[record.Operation](e)
			if err != nil {
				return nil, nil, err
			}
			step := steps[e.Step]
			step.rc = op.RC
			step.sawRC = true
			step.status = op.Status
			steps[e.Step] = step

		case record.KindFact:
			fact, err := record.Decode[record.Fact](e)
			if err != nil {
				return nil, nil, err
			}
			step := steps[e.Step]
			if step.facts == nil {
				step.facts = map[string]record.Fact{}
			}
			// A probe that read the same key twice in one step changed its
			// mind, the way the shell's `tail -1` did. Both readings stay in
			// the events; the later one is the one it stands behind.
			step.facts[fact.Key] = fact
			steps[e.Step] = step

		case record.KindFingerprint:
			fp, err := record.Decode[record.Fingerprint](e)
			if err != nil {
				return nil, nil, err
			}
			step := steps[e.Step]
			step.fingerprint = fp.Result
			steps[e.Step] = step

		case record.KindControlChecked:
			checked, err := record.Decode[record.ControlChecked](e)
			if err != nil {
				return nil, nil, err
			}
			// Only the declaration is taken from here. What the runner
			// observed, and whether the runner thought it was fine, are not
			// evidence about the machine - they are a convenience the judge
			// is not allowed to lean on.
			step := steps[e.Step]
			step.expected = checked.Expected
			if step.label == "" {
				step.label = checked.Label
			}
			steps[e.Step] = step
		}
	}

	// A step that started and never reported left no status behind. Unfinished
	// is all a judge may say about it: naming a reason - probe-fail,
	// infra-fail - would be a diagnosis nothing made, written into a file a
	// finding cites.
	for n, step := range steps {
		if step.status == "" {
			step.status = record.StatusUnfinished
			steps[n] = step
		}
	}
	return steps, order, nil
}

func sorted(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
