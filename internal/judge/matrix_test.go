package judge_test

import (
	"testing"

	"github.com/jmurray2011/testease/internal/judge"
)

func TestMatrixIsVersionedWithTheJudgeItDelegatesTo(t *testing.T) {
	t.Parallel()

	// A census verdict is a rollup: matrix judges each measured cell with
	// expect and reports what expect said, cell by cell. So expect reaching a
	// different conclusion means matrix does too, and leaving matrix on its old
	// number would put two implementations behind one citation key - the thing
	// a version number exists to prevent.
	//
	// The day matrix stops delegating, this goes with that change.
	if got, want := (judge.Matrix{}).Version(), (judge.Expect{}).Version(); got != want {
		t.Errorf("matrix@%s rolls up expect@%s: a census verdict changed under an unchanged version", got, want)
	}
}
