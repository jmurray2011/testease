package judge_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// expect is the successor to `$3 ~ /CONTROL negative/ && $6 != "no"`. That awk
// line read a verdict out of a rendered row; this reads it out of the events,
// which is why a malformed row can no longer produce a wrong verdict from
// correct measurements.

func verdictOf(t *testing.T, steps ...step) judge.Verdict {
	t.Helper()

	_, rec := written(t, steps...)
	v, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return v
}

func TestAnExpectationThatHeld(t *testing.T) {
	t.Parallel()

	v := verdictOf(t, step{
		label:       "CONTROL negative",
		rc:          0,
		fingerprint: record.FingerprintUnchanged,
		expect:      map[string]string{"fingerprint": "unchanged"},
	})

	if v.Verdict != judge.Pass {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Pass)
	}
	if got := v.Steps["CONTROL negative"].Verdict; got != judge.Met {
		t.Errorf("step verdict = %q, want %q", got, judge.Met)
	}
}

func TestANegativeControlThatDrifted(t *testing.T) {
	t.Parallel()

	// The case the whole guard exists for: a step that did nothing, and the
	// instrument says the machine moved anyway.
	v := verdictOf(t, step{
		label:       "CONTROL negative",
		rc:          0,
		fingerprint: record.FingerprintChanged,
		expect:      map[string]string{"fingerprint": "unchanged"},
	})

	if v.Verdict != judge.Regressed {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Regressed)
	}
	got := v.Steps["CONTROL negative"]
	if got.Verdict != judge.Unmet {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unmet)
	}
	if len(got.Because) == 0 {
		t.Fatal("an unmet expectation gave no reason")
	}
	because := strings.Join(got.Because, "; ")
	if !strings.Contains(because, "unchanged") || !strings.Contains(because, "changed") {
		t.Errorf("because = %q, want both sides of the comparison", because)
	}
}

func TestAnExitCodeThatDidNotMatch(t *testing.T) {
	t.Parallel()

	v := verdictOf(t, step{label: "attempt the upgrade", rc: 1, expect: map[string]string{"rc": "0"}})

	if v.Verdict != judge.Regressed {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Regressed)
	}
	if because := strings.Join(v.Steps["attempt the upgrade"].Because, "; "); !strings.Contains(because, "rc") {
		t.Errorf("because = %q, want it to name the field", because)
	}
}

func TestTheJudgeDerivesItsOwnObservation(t *testing.T) {
	t.Parallel()

	// The runner checks a control at run time for fail-fast visibility, and
	// records its own answer. The judge is the authority: it re-derives what
	// happened from the operation and fingerprint events and never reads the
	// runner's conclusion. written() deliberately records a runner answer of
	// ok:true and an observed map full of nonsense.
	v := verdictOf(t, step{
		label:       "CONTROL negative",
		rc:          0,
		fingerprint: record.FingerprintChanged,
		expect:      map[string]string{"fingerprint": "unchanged"},
	})

	if v.Verdict != judge.Regressed {
		t.Errorf("verdict = %q: the judge trusted the runner's ok instead of the events", v.Verdict)
	}
}

func TestAStepThatWasNotMeasuredIsNotJudged(t *testing.T) {
	t.Parallel()

	// A judge never manufactures a verdict for unmeasured evidence.
	v := verdictOf(
		t,
		step{label: "measured one", rc: 0, expect: map[string]string{"rc": "0"}},
		step{label: "never reported", rc: -1, status: record.StatusInfraFail, expect: map[string]string{"rc": "0"}},
	)

	if _, ok := v.Steps["never reported"]; ok {
		t.Error("a step that was not measured got a verdict")
	}
	if got := v.Unjudged["never reported"]; got != string(record.StatusInfraFail) {
		t.Errorf("unjudged = %v, want the step listed with its status", v.Unjudged)
	}
	if v.Verdict != judge.Pass {
		t.Errorf("verdict = %q: an unmeasured step is not a regression", v.Verdict)
	}
}

func TestAStepWithNoExpectationIsNotJudgedEither(t *testing.T) {
	t.Parallel()

	v := verdictOf(
		t,
		step{label: "just does a thing", rc: 3},
		step{label: "and this one is checked", rc: 0, expect: map[string]string{"rc": "0"}},
	)

	if _, ok := v.Steps["just does a thing"]; ok {
		t.Error("a step that declared nothing was judged against something")
	}
	if v.Verdict != judge.Pass {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Pass)
	}
}

func TestARecordWithNothingToJudgeSaysSoRatherThanPassing(t *testing.T) {
	t.Parallel()

	// Never invent success. A record that declared no expectations has not
	// passed anything; it has been read by a judge with nothing to do.
	_, rec := written(t, step{label: "just does a thing", rc: 0})

	_, err := judge.Apply(judge.Expect{}, rec)
	if err == nil {
		t.Fatal("a record with no expectations was judged to pass")
	}
	var nothing *judge.NothingToJudgeError
	if !errors.As(err, &nothing) {
		t.Fatalf("error is %T (%v), want *judge.NothingToJudgeError", err, err)
	}
}

func TestARecordWhoseOnlyExpectationsWentUnmeasured(t *testing.T) {
	t.Parallel()

	_, rec := written(t, step{
		label:  "never reported",
		rc:     -1,
		status: record.StatusInfraFail,
		expect: map[string]string{"rc": "0"},
	})

	_, err := judge.Apply(judge.Expect{}, rec)
	if err == nil {
		t.Fatal("a record with nothing measured was judged")
	}
	var nothing *judge.NothingToJudgeError
	if !errors.As(err, &nothing) {
		t.Fatalf("error is %T (%v), want *judge.NothingToJudgeError", err, err)
	}
	if len(nothing.Unjudged) == 0 {
		t.Error("the refusal does not say what went unjudged")
	}
}

func TestAnUnknownExpectationFieldIsSaidOutLoud(t *testing.T) {
	t.Parallel()

	// expect covers rc and fingerprint. Anything else is a scenario asking for
	// something this judge does not do, and silently ignoring it would let a
	// declared check quietly never run.
	v := verdictOf(t, step{
		label:  "a step",
		rc:     0,
		expect: map[string]string{"rc": "0", "phase-of-the-moon": "waxing"},
	})

	got := v.Steps["a step"]
	if got.Verdict != judge.Unverifiable {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unverifiable)
	}
	if because := strings.Join(got.Because, "; "); !strings.Contains(because, "phase-of-the-moon") {
		t.Errorf("because = %q, want it to name the field it cannot check", because)
	}
}

// --- facts -----------------------------------------------------------------
//
// A fact is the structured channel: what a probe read off the machine, on fd 3,
// as a key and a value. Facts are not claims. A claim is testimony and a judge
// that parses testimony has rebuilt the coupling this design exists to remove;
// a fact is a reading, and reading it is what a judge is for.
//
// The scenario that forced this: an upgrade refused on a precondition leaves
// the service stopped. The shell that measured it packed svc, pkg and the at
// queue into one claim line and grepped them back out in a verdict block, and
// its own comments record two ways that went wrong. The readings are facts now,
// so the expectation is declared beside the step and checked from the events.

func reading(key, value string) record.Fact {
	return record.Fact{Key: key, Value: &value, Probe: "systemctl is-active"}
}

func TestAFactExpectationThatHeld(t *testing.T) {
	t.Parallel()

	v := verdictOf(t, step{
		label:  "PRECONDITION: it is serving before anything is attempted",
		rc:     0,
		facts:  []record.Fact{reading("svc.active", "active")},
		expect: map[string]string{"fact.svc.active": "active"},
	})

	if v.Verdict != judge.Pass {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Pass)
	}
	if got := v.Steps["PRECONDITION: it is serving before anything is attempted"].Verdict; got != judge.Met {
		t.Errorf("step verdict = %q, want %q", got, judge.Met)
	}
}

func TestAFactExpectationThatDidNotHold(t *testing.T) {
	t.Parallel()

	// The soundness gate the shell verdict opened with: if the box was not
	// serving before the attempt, nothing measured after it is about the
	// upgrade. Under the old harness that gate was an awk line over a rendered
	// row; here it is the declaration the step itself made.
	v := verdictOf(t, step{
		label:  "PRECONDITION: it is serving before anything is attempted",
		rc:     0,
		facts:  []record.Fact{reading("svc.active", "inactive")},
		expect: map[string]string{"fact.svc.active": "active"},
	})

	if v.Verdict != judge.Regressed {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Regressed)
	}
	got := v.Steps["PRECONDITION: it is serving before anything is attempted"]
	if got.Verdict != judge.Unmet {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unmet)
	}
	because := strings.Join(got.Because, "; ")
	for _, want := range []string{"svc.active", "active", "inactive"} {
		if !strings.Contains(because, want) {
			t.Errorf("because = %q, want it to carry %q", because, want)
		}
	}
}

func TestAFactNobodyReadIsUnverifiableNotUnmet(t *testing.T) {
	t.Parallel()

	// A reading that was never taken did not fail. Calling it unmet would be
	// the judge stating a domain outcome with no measurement behind it, which
	// is the impersonation the whole design is built to prevent.
	v := verdictOf(t, step{
		label:  "AFTER: what the refused upgrade left behind",
		rc:     0,
		expect: map[string]string{"fact.svc.active": "active"},
	})

	if v.Verdict != judge.Unverified {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Unverified)
	}
	got := v.Steps["AFTER: what the refused upgrade left behind"]
	if got.Verdict != judge.Unverifiable {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unverifiable)
	}
	if because := strings.Join(got.Because, "; "); !strings.Contains(because, "svc.active") {
		t.Errorf("because = %q, want it to name the reading nobody took", because)
	}
}

func TestAFactWhoseProbeFailedIsUnverifiable(t *testing.T) {
	t.Parallel()

	// A fact whose probe failed records a null value and the error. That is
	// evidence - it is just not a reading of the machine, so it cannot disagree
	// with an expectation any more than an absent one can.
	v := verdictOf(t, step{
		label:  "AFTER: what the refused upgrade left behind",
		rc:     0,
		facts:  []record.Fact{{Key: "db.tables", Error: "psql would not answer"}},
		expect: map[string]string{"fact.db.tables": "28"},
	})

	got := v.Steps["AFTER: what the refused upgrade left behind"]
	if got.Verdict != judge.Unverifiable {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unverifiable)
	}
	if because := strings.Join(got.Because, "; "); !strings.Contains(because, "psql would not answer") {
		t.Errorf("because = %q, want it to carry why the reading is missing", because)
	}
}

func TestAFactIsComparedVerbatim(t *testing.T) {
	t.Parallel()

	// Values are strings and numeric interpretation belongs to a judge that
	// says it is doing arithmetic. This one compares what was written down. A
	// probe that recorded 000 for an http code did not observe 0.
	v := verdictOf(t, step{
		label:  "AFTER: what the refused upgrade left behind",
		rc:     0,
		facts:  []record.Fact{reading("http.8080", "000")},
		expect: map[string]string{"fact.http.8080": "0"},
	})

	if got := v.Steps["AFTER: what the refused upgrade left behind"].Verdict; got != judge.Unmet {
		t.Errorf("step verdict = %q, want %q: 000 was read as 0", got, judge.Unmet)
	}
}

func TestTheLastReadingOfAFactWins(t *testing.T) {
	t.Parallel()

	// A probe that reads the same key twice in one step has changed its mind,
	// the way the shell's `tail -1` did. The later reading is the one it stands
	// behind; both stay in the events either way.
	v := verdictOf(t, step{
		label:  "RETRIED: does the scheduled restart actually land",
		rc:     0,
		facts:  []record.Fact{reading("svc.active", "inactive"), reading("svc.active", "active")},
		expect: map[string]string{"fact.svc.active": "active"},
	})

	if got := v.Steps["RETRIED: does the scheduled restart actually land"].Verdict; got != judge.Met {
		t.Errorf("step verdict = %q, want %q", got, judge.Met)
	}
}

func TestAFactBelongsToTheStepThatReadIt(t *testing.T) {
	t.Parallel()

	// An expectation is about the step that declared it. Reaching back to a
	// reading another step took would be cross-step reasoning, which is a
	// different judge's job: this one would be answering a question about the
	// run using evidence the step in front of it never produced.
	v := verdictOf(
		t,
		step{
			label:  "PRECONDITION: it is serving before anything is attempted",
			rc:     0,
			facts:  []record.Fact{reading("svc.active", "active")},
			expect: map[string]string{"fact.svc.active": "active"},
		},
		step{
			label:  "AFTER: what the refused upgrade left behind",
			rc:     0,
			expect: map[string]string{"fact.svc.active": "active"},
		},
	)

	if got := v.Steps["AFTER: what the refused upgrade left behind"].Verdict; got != judge.Unverifiable {
		t.Errorf("step verdict = %q, want %q: it borrowed another step's reading", got, judge.Unverifiable)
	}
}

func TestAnUnmetFactOutranksAnUnverifiableOne(t *testing.T) {
	t.Parallel()

	v := verdictOf(t, step{
		label: "AFTER: what the refused upgrade left behind",
		rc:    0,
		facts: []record.Fact{reading("svc.active", "inactive")},
		expect: map[string]string{
			"fact.svc.active": "active",
			"fact.db.tables":  "28",
		},
	})

	got := v.Steps["AFTER: what the refused upgrade left behind"]
	if got.Verdict != judge.Unmet {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unmet)
	}
	// Both are reported: a reading that disagreed and a reading nobody took are
	// different facts about the run, and the second does not stop being true.
	if because := strings.Join(got.Because, "; "); !strings.Contains(because, "db.tables") {
		t.Errorf("because = %q, want the unverifiable one kept too", because)
	}
}

func TestExpectIsVersioned(t *testing.T) {
	t.Parallel()

	if got := (judge.Expect{}).Version(); got == "" {
		t.Error("a judge with no version cannot be cited")
	}
}

func TestAnExpectationWithNoReadingIsUnverifiableNotRegressed(t *testing.T) {
	t.Parallel()

	// A target with no fingerprint instrument cannot answer a control at all.
	// That is not the product regressing - it is the judge having nothing to
	// judge with, and saying "regressed" would be a domain conclusion drawn
	// from an absent reading.
	v := verdictOf(t, step{
		label:       "CONTROL negative",
		rc:          0,
		fingerprint: record.FingerprintUnavailable,
		expect:      map[string]string{"fingerprint": "unchanged"},
	})

	if v.Verdict != judge.Unverified {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Unverified)
	}
	got := v.Steps["CONTROL negative"]
	if got.Verdict != judge.Unverifiable {
		t.Errorf("step verdict = %q, want %q", got.Verdict, judge.Unverifiable)
	}
	if because := strings.Join(got.Because, "; "); !strings.Contains(because, "unavailable") {
		t.Errorf("because = %q, want it to name what was missing", because)
	}
}

func TestAnInstrumentThatFailedIsAlsoUnverifiable(t *testing.T) {
	t.Parallel()

	v := verdictOf(t, step{
		label:       "CONTROL negative",
		rc:          0,
		fingerprint: record.FingerprintErrored,
		expect:      map[string]string{"fingerprint": "unchanged"},
	})

	if got := v.Steps["CONTROL negative"].Verdict; got != judge.Unverifiable {
		t.Errorf("step verdict = %q, want %q", got, judge.Unverifiable)
	}
}

func TestARealRegressionStillOutranksAnUnverifiableOne(t *testing.T) {
	t.Parallel()

	// One step nobody could check does not soften another that genuinely did
	// not hold.
	v := verdictOf(
		t,
		step{
			label: "could not check", rc: 0, fingerprint: record.FingerprintUnavailable,
			expect: map[string]string{"fingerprint": "unchanged"},
		},
		step{label: "did not hold", rc: 1, expect: map[string]string{"rc": "0"}},
	)

	if v.Verdict != judge.Regressed {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Regressed)
	}
}

func TestAReasonMakesAnInvisibleDifferenceVisible(t *testing.T) {
	t.Parallel()

	// Values are compared verbatim, so a trailing CR is a real difference. A
	// reason that renders both sides identically - "expected 9.0.106, observed
	// 9.0.106" - is a record that lies about why it disagreed.
	v := verdictOf(t, step{
		label:  "BASELINE: tomcat version",
		rc:     0,
		facts:  []record.Fact{reading("tomcat.version", "9.0.106\r")},
		expect: map[string]string{"fact.tomcat.version": "9.0.106"},
	})

	because := strings.Join(v.Steps["BASELINE: tomcat version"].Because, "; ")
	if !strings.Contains(because, `\r`) {
		t.Errorf("because = %q, want the difference visible", because)
	}
}
