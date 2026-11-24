package main

import (
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/browse"
)

// The Runs screen's choices, and the words that mean "any".
const (
	anyScenario = "Every scenario"
	anyOutcome  = "Any outcome"
	anyTime     = "Any time"
	sinceToday  = "Today (UTC)"
	sinceWeek   = "Last 7 days"
	sinceMonth  = "Last 30 days"
)

var sinceChoices = []string{anyTime, sinceToday, sinceWeek, sinceMonth}

func outcomeChoices() []string {
	return []string{
		anyOutcome, outcomePassed.String(), outcomeFailed.String(), outcomeBroken.String(),
		outcomeUnjudged.String(), outcomeIncomplete.String(),
	}
}

// Where the Runs screen keeps its choices. The typed text is not kept: it is
// a search, not a view.
const (
	prefRunsScenario = "runs-scenario"
	prefRunsOutcome  = "runs-outcome"
	prefRunsSince    = "runs-since"
)

// runFilter narrows the Runs screen. An empty part matches everything.
type runFilter struct {
	Text, Scenario, Outcome, Since string
}

func (f runFilter) matches(r browse.Run, status func(browse.Run) string, now time.Time) bool {
	if f.Scenario != "" && f.Scenario != anyScenario && r.Scenario != f.Scenario {
		return false
	}
	if f.Outcome != "" && f.Outcome != anyOutcome && outcomeOf(r).String() != f.Outcome {
		return false
	}
	if from := sinceStamp(f.Since, now); from != "" && takenAt(r.RunID) < from {
		return false
	}
	needle := strings.ToLower(strings.TrimSpace(f.Text))
	return needle == "" ||
		strings.Contains(strings.ToLower(r.RunID), needle) ||
		strings.Contains(strings.ToLower(r.Scenario), needle) ||
		strings.Contains(strings.ToLower(status(r)), needle)
}

// active says whether anything narrows the listing.
func (f runFilter) active() bool {
	return strings.TrimSpace(f.Text) != "" ||
		(f.Scenario != "" && f.Scenario != anyScenario) ||
		(f.Outcome != "" && f.Outcome != anyOutcome) ||
		(f.Since != "" && f.Since != anyTime)
}

// sinceStamp is the earliest run id stamp a Since choice lets through.
func sinceStamp(since string, now time.Time) string {
	day := now.UTC().Truncate(24 * time.Hour)
	switch since {
	case sinceToday:
	case sinceWeek:
		day = day.AddDate(0, 0, -6)
	case sinceMonth:
		day = day.AddDate(0, 0, -29)
	default:
		return ""
	}
	return day.Format("20060102T150405Z")
}
