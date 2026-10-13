package main

import (
	"testing"

	"github.com/jmurray2011/testease/internal/reap"
	"github.com/jmurray2011/testease/internal/target"
)

// The ownership labels are spelled twice on purpose: target may not import
// reap and reap may import nothing internal, so each carries its own copy.
// Two spellings of one key would make every target unprovable, which is why
// the binary - the one place both sides meet - holds them together.
func TestReapAndTargetAgreeOnTheOwnershipLabels(t *testing.T) {
	t.Parallel()

	pairs := map[string]string{
		target.LabelRunner:  reap.LabelRunner,
		target.LabelRun:     reap.LabelRun,
		target.LabelPID:     reap.LabelPID,
		target.LabelCreated: reap.LabelCreated,
		target.LabelBackend: reap.LabelBackend,
		target.LabelHost:    reap.LabelHost,
		target.LabelCell:    reap.LabelCell,
	}
	for wrote, decided := range pairs {
		if wrote != decided {
			t.Errorf("providers write %q and reap decides from %q", wrote, decided)
		}
	}
}
