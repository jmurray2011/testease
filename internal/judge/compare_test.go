package judge_test

import (
	"testing"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// The question compare exists for is one no step can answer about itself:
// whether two independent installs produced the same credential. The expect
// judge refuses it on purpose - an expectation is about the step that declared
// it - so this is the judge that says out loud that it is asking.

func hostReading(label, key, value string) step {
	return step{label: label, facts: []record.Fact{{Key: key, Value: &value}}}
}

func TestTwoReadingsThatDiffer(t *testing.T) {
	t.Parallel()

	_, rec := written(
		t,
		hostReading("hostA creds", "creds.alpha", "aaa"),
		hostReading("hostB creds", "creds.alpha", "bbb"),
	)

	got, err := judge.Compare{Differ: []string{"creds.alpha"}}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Verdict != judge.Pass {
		t.Errorf("a key that must differ and did: %q (%v)", got.Verdict, got.Steps)
	}

	got, err = judge.Compare{Same: []string{"creds.alpha"}}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Verdict != judge.Regressed {
		t.Errorf("a key that must match and did not: %q", got.Verdict)
	}
}

func TestOneReadingIsNotAComparison(t *testing.T) {
	t.Parallel()

	// Calling a single reading a match would be the judge concluding from a
	// measurement nobody took, which is the failure mode the whole system is
	// built against.
	_, rec := written(t, hostReading("hostA creds", "creds.beta", "aaa"))

	got, err := judge.Compare{Same: []string{"creds.beta"}}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Verdict != judge.Unverified {
		t.Errorf("verdict = %q, want %q", got.Verdict, judge.Unverified)
	}
}

func TestARealDisagreementOutranksAMissingReading(t *testing.T) {
	t.Parallel()

	_, rec := written(
		t,
		hostReading("hostA creds", "creds.beta", "shipped"),
		hostReading("hostB creds", "creds.beta", "generated"),
		hostReading("hostA docs", "creds.alpha", "only-one"),
	)

	got, err := judge.Compare{
		Same:   []string{"creds.beta"},
		Differ: []string{"creds.alpha"},
	}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Verdict != judge.Regressed {
		t.Errorf("verdict = %q, want %q", got.Verdict, judge.Regressed)
	}
}

func TestAnUnmeasuredStepIsNotEvidence(t *testing.T) {
	t.Parallel()

	// A step whose probe died left a reading behind that is not a reading of
	// the machine. Comparing it against a real one would manufacture a
	// disagreement out of infrastructure weather.
	dead := hostReading("hostB creds", "creds.gamma", "aaa")
	dead.status = record.StatusProbeFail

	_, rec := written(t, hostReading("hostA creds", "creds.gamma", "bbb"), dead)

	got, err := judge.Compare{Same: []string{"creds.gamma"}}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Verdict != judge.Unverified {
		t.Errorf("verdict = %q, want %q", got.Verdict, judge.Unverified)
	}
}

func TestACompareThatNamesNothing(t *testing.T) {
	t.Parallel()

	_, rec := written(t, hostReading("hostA creds", "creds.beta", "aaa"))
	if _, err := (judge.Compare{}).Judge(rec); err == nil {
		t.Error("a compare judge naming no keys reached a verdict about nothing")
	}
}
