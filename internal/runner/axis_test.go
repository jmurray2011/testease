package runner_test

import (
	"errors"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/target"
)

func TestBothFailureKindsAgree(t *testing.T) {
	out, rec, _, err := sweepRun(t, census{
		values: []string{"1.0", "2.0"},
		perCell: func(from string) (target.Result, error) {
			if from == "1.0" {
				return target.Result{}, errors.New("no boot")
			}
			return target.Result{RC: 0, TimedOut: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	f := only[record.RunFinished](t, rec, record.KindRunFinished)
	t.Logf("outcome=%q run-finished=%q cells=%v", out.Status, f.Status, f.Cells)
	if out.Status != record.StatusProbeFail {
		t.Errorf("status = %q, want probe-fail to dominate as the exit code does", out.Status)
	}
}
