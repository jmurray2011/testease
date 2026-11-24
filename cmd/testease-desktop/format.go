package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// statusRunning is shown for a record whose writer still holds it. It is not a
// status the record carries: it is what the lock says right now.
const statusRunning = "running"

// statusOf is what the run measured, or, for a record that has not closed,
// whether something is still writing it.
func statusOf(r browse.Run) string {
	if r.Status == string(record.StatusUnfinished) {
		if held, err := record.WriterLocked(r.Dir); err == nil && held {
			return statusRunning
		}
	}
	return r.Status
}

// statusKind picks a status pill's colours. A status is what was measured, not
// a conclusion, so only the statuses that say the measurement itself went
// wrong are marked.
func statusKind(status string) pillKind {
	switch status {
	case string(record.StatusProbeFail), string(record.StatusInfraFail), browse.Unreadable:
		return pillBad
	case statusRunning:
		return pillActive
	default:
		return pillNeutral
	}
}

func verdictKind(v browse.Verdict) pillKind {
	switch {
	case !v.Current || !v.Shipped:
		return pillNeutral
	case v.Passed:
		return pillGood
	default:
		return pillBad
	}
}

func verdictName(v browse.Verdict) string {
	return v.Judge + "@" + v.Version
}

// verdictLine is one verdict as a listing shows it. A verdict over other bytes
// is not this record's verdict and says so.
func verdictLine(v browse.Verdict) string {
	line := verdictName(v) + " " + v.Verdict
	if !v.Current {
		line += ", over earlier events"
	}
	return line
}

// verdictSummary is the listing's verdict column: the record's own verdicts
// first, then any reached over earlier events.
func verdictSummary(vs []browse.Verdict) (string, pillKind) {
	if len(vs) == 0 {
		return "not judged", pillNeutral
	}
	ordered := append([]browse.Verdict(nil), vs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Current && !ordered[j].Current })
	text := verdictLine(ordered[0])
	if len(ordered) > 1 {
		text += fmt.Sprintf(" · +%d", len(ordered)-1)
	}
	return text, verdictKind(ordered[0])
}

// fateText says what became of a target in words. An intent to release with no
// outcome after it means the release was started and nobody recorded how it
// ended, which is not the same as released.
func fateText(action string) string {
	switch action {
	case "released":
		return "released"
	case "retained":
		return "kept"
	case "retain-failed":
		return "retained after failure"
	case "release-intended":
		return "release started, outcome not recorded"
	case "":
		return "no disposition recorded"
	default:
		return action
	}
}

func fatesText(targets []browse.Target) string {
	if len(targets) == 0 {
		return "—"
	}
	seen := map[string]bool{}
	var fates []string
	for _, t := range targets {
		f := fateText(t.Fate)
		if !seen[f] {
			seen[f] = true
			fates = append(fates, f)
		}
	}
	return strings.Join(fates, ", ")
}

func measuredText(r browse.Run) string {
	switch {
	case r.Cells > 0:
		return plural(r.Cells, "cell")
	case r.Status == browse.Unreadable:
		return "—"
	default:
		return plural(r.Steps, "step")
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// stamp finds the UTC timestamp a run id carries by contract, so records sort
// by when they were taken whatever scenario they belong to. A batch's named
// run carries a suffix after it.
var stamp = regexp.MustCompile(`\d{8}T\d{6}Z`)

func takenAt(runID string) string {
	return stamp.FindString(runID)
}
