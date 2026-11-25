package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/campaign"
)

// boardState is where one thing on a campaign's board has got.
type boardState int

const (
	boardWaiting boardState = iota // a run the campaign has still to start
	boardRunning                   // going now
	boardDone                      // finished, with nothing to conclude
	boardBad                       // finished with a non-zero exit
	boardPassed                    // a run whose verdict passed
	boardFailed                    // a run whose verdict did not
	boardBroken                    // a run whose measurement broke
)

// boardItem is one thing a campaign did or has still to do.
type boardItem struct {
	Label string
	Word  string
	State boardState
	RunID string
}

func (s boardState) look() tileLook {
	switch s {
	case boardWaiting:
		return lookEmpty
	case boardRunning:
		return lookActive
	case boardPassed:
		return lookPassed
	case boardFailed, boardBad:
		return lookFailed
	case boardBroken:
		return lookBroken
	default:
		return lookNeutral
	}
}

// campaignBoard lays out what a campaign did, in order, and, while it runs,
// the runs it has still to start. A run is read from its own record when
// there is one, so its colour is its verdict, not its exit code.
func campaignBoard(c browse.Campaign, runOf func(id string) (browse.Run, bool)) []boardItem {
	var items []boardItem
	runs := 0
	for _, a := range c.Activities {
		it := boardItem{Label: activityName(a), RunID: a.RunID}
		isRun := a.Kind != "hook" && a.Name == "run"
		if isRun {
			runs++
		}
		switch {
		case !a.Finished:
			it.State, it.Word = boardRunning, "running"
		case isRun && a.RunID != "":
			if r, ok := runOf(a.RunID); ok {
				it.State, it.Word = runState(outcomeOf(r))
				break
			}
			fallthrough
		default:
			if a.ExitCode != 0 {
				it.State, it.Word = boardBad, fmt.Sprintf("exit %d", a.ExitCode)
			} else {
				it.State, it.Word = boardDone, "done"
			}
		}
		items = append(items, it)
	}
	if c.Status == campaign.StatusRunning {
		for i := runs + 1; i <= c.Repeat; i++ {
			items = append(items, boardItem{Label: fmt.Sprintf("run %d", i), Word: "waiting", State: boardWaiting})
		}
	}
	return items
}

func runState(o outcome) (boardState, string) {
	switch o {
	case outcomePassed:
		return boardPassed, "passed"
	case outcomeFailed:
		return boardFailed, "failed"
	case outcomeBroken:
		return boardBroken, "broke"
	case outcomeIncomplete:
		return boardDone, "unfinished"
	default:
		return boardDone, "not judged"
	}
}

// boardSummary says how far the campaign's runs have got.
func boardSummary(c browse.Campaign, items []boardItem) string {
	total, done := 0, 0
	counts := map[string]int{}
	going := ""
	for _, it := range items {
		if !strings.HasPrefix(it.Label, "run ") {
			continue
		}
		total++
		switch it.State {
		case boardRunning:
			going = it.Label
		case boardWaiting:
		default:
			done++
			counts[it.Word]++
		}
	}
	total = max(total, c.Repeat)
	text := fmt.Sprintf("%d of %s done", done, plural(total, "run"))
	var parts []string
	for _, w := range []string{"passed", "failed", "broke", "not judged", "unfinished"} {
		if counts[w] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[w], w))
		}
	}
	if n := done - counts["passed"] - counts["failed"] - counts["broke"] - counts["not judged"] - counts["unfinished"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d without a record", n))
	}
	if len(parts) > 0 {
		text += ": " + strings.Join(parts, ", ")
	}
	if going != "" {
		text += " · " + going + " going"
	}
	return text
}

// Sizes of a board's tiles.
const (
	boardTileW = 118
	boardTileH = 46
)

// boardView is a campaign's board: a row of tiles, one per thing it did,
// and a line on how far its runs have got. A run's tile opens its record.
type boardView struct {
	runsDir string
	items   []boardItem
	summary *widget.Label
	row     *fyne.Container
	onOpen  func(dir string)
	content fyne.CanvasObject
}

func newBoardView(runsDir string, onOpen func(string)) *boardView {
	v := &boardView{runsDir: runsDir, onOpen: onOpen, summary: widget.NewLabel(""), row: container.NewHBox()}
	v.content = container.NewVBox(v.summary, container.NewHScroll(v.row))
	return v
}

// set draws the board for a campaign as it now stands.
func (v *boardView) set(c browse.Campaign) {
	v.items = campaignBoard(c, func(id string) (browse.Run, bool) {
		if !filepath.IsLocal(id) {
			return browse.Run{}, false
		}
		d, err := browse.Open(filepath.Join(v.runsDir, id))
		return d.Run, err == nil
	})
	v.summary.SetText(boardSummary(c, v.items))
	v.row.Objects = v.row.Objects[:0]
	for _, it := range v.items {
		var tap func()
		if id := it.RunID; id != "" && filepath.IsLocal(id) && v.onOpen != nil {
			tap = func() { v.onOpen(filepath.Join(v.runsDir, id)) }
		}
		v.row.Add(sized(boardTileW, boardTileH, newOutcomeTile(it.Label, it.Word, it.State.look(), tap)))
	}
	v.row.Refresh()
}
