package record_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
)

// The run id shape is a contract: findings cite it, corpora sort on it, and a
// bare timestamp is a corpus defect.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+-[0-9]{8}T[0-9]{6}Z$`)

func TestRunIDShape(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 12, 14, 3, 22, 184_000_000, time.UTC)
	got := record.RunID("aborted-upgrade-leaves-it-down", at)
	want := "aborted-upgrade-leaves-it-down-20260812T140322Z"

	if got != want {
		t.Errorf("RunID = %q, want %q", got, want)
	}
	if !runIDPattern.MatchString(got) {
		t.Errorf("RunID %q does not match the id contract", got)
	}
}

func TestRunIDStampsUTCWhateverTheZone(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("ahead", 5*3600)
	at := time.Date(2026, 8, 12, 19, 3, 22, 0, zone) // 14:03:22Z

	if got, want := record.RunID("s", at), "s-20260812T140322Z"; got != want {
		t.Errorf("RunID = %q, want %q", got, want)
	}
}

func TestStepSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		seq   int
		label string
		want  string
	}{
		{1, "install old rung", "01-install-old-rung"},
		{3, "CONTROL negative", "03-CONTROL-negative"},
		{12, "attempt upgrade", "12-attempt-upgrade"},
		{1, "a.b_c", "01-a-b-c"},
		{1, "  padded  ", "01-padded"},
		{1, "runs---squeezed", "01-runs-squeezed"},
		{1, "tab\there", "01-tab-here"},
		{1, "!!!", "01-"},
	}

	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			t.Parallel()

			if got := record.StepSlug(c.seq, c.label); got != c.want {
				t.Errorf("StepSlug(%d, %q) = %q, want %q", c.seq, c.label, got, c.want)
			}
		})
	}
}

func TestStepPathsAreRecordRelative(t *testing.T) {
	t.Parallel()

	out, errPath := record.StepPaths(3, "install old rung")
	if want := "steps/03-install-old-rung.out"; out != want {
		t.Errorf("stdout path = %q, want %q", out, want)
	}
	if want := "steps/03-install-old-rung.err"; errPath != want {
		t.Errorf("stderr path = %q, want %q", errPath, want)
	}
}
