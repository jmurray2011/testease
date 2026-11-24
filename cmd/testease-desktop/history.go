package main

import (
	"sort"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// outcome is what one run comes to in a scenario's history. Only a verdict
// says passed or failed; the rest say why there is no verdict to show.
type outcome int

const (
	outcomeUnjudged   outcome = iota // measured, and no shipped judge's verdict stands over it
	outcomePassed                    // every current shipped verdict passed
	outcomeFailed                    // a current shipped verdict did not pass
	outcomeBroken                    // the measurement itself went wrong
	outcomeIncomplete                // stopped, or still being written
)

func (o outcome) String() string {
	return [...]string{"not judged", "passed", "failed", "measurement broke", "did not finish"}[o]
}

// outcomeOf reads a run's outcome. A verdict over earlier events, or one from
// an external judge whose pass set is its caller's to say, is not counted.
func outcomeOf(r browse.Run) outcome {
	switch r.Status {
	case string(record.StatusProbeFail), string(record.StatusInfraFail), browse.Unreadable:
		return outcomeBroken
	case string(record.StatusInterrupted), string(record.StatusUnfinished):
		return outcomeIncomplete
	}
	result := outcomeUnjudged
	for _, v := range r.Verdicts {
		if !v.Current || !v.Shipped {
			continue
		}
		if !v.Passed {
			return outcomeFailed
		}
		result = outcomePassed
	}
	return result
}

// historyLength is how many runs a scenario's strip shows.
const historyLength = 20

type historyPoint struct {
	RunID, Dir string
	Outcome    outcome
}

// scenarioHistory is one scenario's runs, oldest first, with counts over all
// of them.
type scenarioHistory struct {
	Scenario       string
	Points         []historyPoint // the newest historyLength runs
	Runs           int
	Judged, Passed int
	Changes        int    // judged outcomes that differ from the judged run before
	Last           string // when the newest run was taken, as its run id says
}

// PassRate is the share of judged runs that passed; ok is false when nothing
// was judged, because no rate is not a rate of zero.
func (h scenarioHistory) PassRate() (rate float64, ok bool) {
	if h.Judged == 0 {
		return 0, false
	}
	return float64(h.Passed) / float64(h.Judged), true
}

// Flakiness is the share of chances a judged outcome had to change that it
// took: 0 is steady, 1 flips every time.
func (h scenarioHistory) Flakiness() float64 {
	if h.Judged < 2 {
		return 0
	}
	return float64(h.Changes) / float64(h.Judged-1)
}

// histories groups runs by scenario, the scenario run most recently first.
func histories(runs []browse.Run) []scenarioHistory {
	ordered := append([]browse.Run(nil), runs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := takenAt(ordered[i].RunID), takenAt(ordered[j].RunID)
		if a != b {
			return a < b
		}
		return ordered[i].RunID < ordered[j].RunID
	})
	byName := map[string]*scenarioHistory{}
	var names []string
	last := map[string]outcome{}
	for _, r := range ordered {
		h, ok := byName[r.Scenario]
		if !ok {
			h = &scenarioHistory{Scenario: r.Scenario}
			byName[r.Scenario] = h
			names = append(names, r.Scenario)
		}
		o := outcomeOf(r)
		h.Runs++
		h.Points = append(h.Points, historyPoint{RunID: r.RunID, Dir: r.Dir, Outcome: o})
		if len(h.Points) > historyLength {
			h.Points = h.Points[1:]
		}
		if at := takenAt(r.RunID); at != "" {
			h.Last = at
		}
		if o == outcomePassed || o == outcomeFailed {
			h.Judged++
			if o == outcomePassed {
				h.Passed++
			}
			if prev, seen := last[r.Scenario]; seen && prev != o {
				h.Changes++
			}
			last[r.Scenario] = o
		}
	}
	out := make([]scenarioHistory, 0, len(names))
	for _, n := range names {
		out = append(out, *byName[n])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out
}
