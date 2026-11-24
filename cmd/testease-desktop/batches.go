package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/campaign"
)

// The Batches screen's columns.
const (
	bcolBatch = iota
	bcolStatus
	bcolTests
	bcolJudgment
)

var (
	batchTitles = []string{"Batch", "Status", "Tests", "Judgment"}
	batchWidths = []float32{340, 105, 90, 160}
)

// batchesView lists the batches under a runs directory, newest first.
type batchesView struct {
	dir    string
	onOpen func(browse.Batch)

	all     []browse.Batch
	count   *widget.Label
	message *widget.Label
	table   *widget.Table
	content fyne.CanvasObject
	run     *action
}

func newBatchesView(dir string, onOpen func(browse.Batch)) *batchesView {
	v := &batchesView{dir: dir, onOpen: onOpen, count: widget.NewLabel(""), message: message(), run: newAction("Run batch…", theme.MediaPlayIcon())}
	v.table = listing(func() int { return len(v.all) }, batchTitles, batchWidths, bcolBatch, v.cell,
		func(row int) { v.onOpen(v.all[row]) })
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), v.reload)
	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Batches", v.count, v.run.Button, refresh), widget.NewSeparator(), v.message),
		container.NewVBox(widget.NewSeparator(), note("A batch's status is what its runs measured. Its judgment is what judges said of them, read from the judgment file as written.")),
		nil, nil, v.table,
	)
	v.reload()
	return v
}

func (v *batchesView) reload() {
	v.all = nil
	switch all, err := browse.Batches(v.dir); {
	case v.dir == "":
		showMessage(v.message, "Choose the runs directory to see its batches.")
	case err != nil:
		showMessage(v.message, fmt.Sprintf("Could not read the batches in %s: %v", v.dir, err))
	case len(all) == 0:
		showMessage(v.message, "No batches under "+v.dir+". A batch is started with testease batch.")
	default:
		v.all = all
		v.message.Hide()
	}
	ids := make([]string, len(v.all))
	for i, b := range v.all {
		ids[i] = b.ID
	}
	fitColumn(v.table, bcolBatch, batchWidths[bcolBatch], ids)
	v.count.SetText(plural(len(v.all), "batch"))
	v.table.Refresh()
}

func (v *batchesView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.all) {
		return "", pillNeutral, false
	}
	b := v.all[row]
	switch col {
	case bcolBatch:
		return b.ID, pillNeutral, false
	case bcolStatus:
		return b.Status, groupStatusKind(b.Status), true
	case bcolTests:
		if b.Status == browse.Unreadable {
			return "—", pillNeutral, false
		}
		return plural(len(b.Tests), "test"), pillNeutral, false
	case bcolJudgment:
		switch {
		case b.Judged:
			return b.JudgmentStatus, groupStatusKind(b.JudgmentStatus), true
		case b.Problem != "" && b.Status != browse.Unreadable:
			return "judgment refused", pillBad, true
		default:
			return "not judged", pillNeutral, false
		}
	}
	return "", pillNeutral, false
}

func (v *batchesView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}

// The batch screen's columns.
const (
	tcolTest = iota
	tcolStatus
	tcolRun
	tcolVerdict
)

var (
	testTitles = []string{"Test", "Status", "Run", "Verdict"}
	testWidths = []float32{200, 105, 420, 240}
)

// batchView is one batch opened: its tests, the run each started, and the
// verdict its judgment gave each.
type batchView struct {
	runsDir string
	b       browse.Batch
	openRun func(dir string)

	problem *widget.Label
	table   *widget.Table
	content fyne.CanvasObject
	judge   *action
}

func newBatchView(runsDir string, b browse.Batch, back func(), openRun func(string)) *batchView {
	v := &batchView{runsDir: runsDir, b: b, openRun: openRun, problem: message(), judge: newAction("Judge batch", theme.ConfirmIcon())}

	top := backHeader("Batches", back, b.ID,
		container.NewCenter(newPill(b.Status, groupStatusKind(b.Status))), v.judge.Button, openFolderButton(b.Dir))
	meta := note(batchMeta(b))
	if b.Problem != "" {
		showMessage(v.problem, b.Problem)
	}
	v.table = listing(func() int { return len(v.b.Tests) }, testTitles, testWidths, tcolRun, v.cell, v.open)
	ids := make([]string, len(b.Tests))
	for i, t := range b.Tests {
		ids[i] = t.RunID
	}
	fitColumn(v.table, tcolRun, testWidths[tcolRun], ids)

	v.content = container.NewBorder(
		container.NewVBox(container.NewPadded(container.NewVBox(top, meta)), widget.NewSeparator(), v.problem),
		container.NewVBox(widget.NewSeparator(), note("Pick a test to open the record it started.")),
		nil, nil, v.table,
	)
	return v
}

func batchMeta(b browse.Batch) string {
	parts := []string{plural(len(b.Tests), "test"), fmt.Sprintf("exit %d", b.ExitCode)}
	if b.Judged {
		parts = append(parts, "judgment "+b.JudgmentStatus)
	} else {
		parts = append(parts, "not judged")
	}
	return strings.Join(parts, "    ")
}

// open opens the record a test started. A test that started none - skipped,
// or refused before it ran - has nothing to open.
func (v *batchView) open(row int) {
	if row < 0 || row >= len(v.b.Tests) {
		return
	}
	id := v.b.Tests[row].RunID
	if id == "" || !filepath.IsLocal(id) {
		return
	}
	v.openRun(filepath.Join(v.runsDir, id))
}

// reload reads the batch again; a batch that is still running fills in.
func (v *batchView) reload() {
	all, err := browse.Batches(v.runsDir)
	if err != nil {
		return
	}
	for _, b := range all {
		if b.ID == v.b.ID {
			v.b = b
			v.table.Refresh()
			return
		}
	}
}

func (v *batchView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.b.Tests) {
		return "", pillNeutral, false
	}
	t := v.b.Tests[row]
	switch col {
	case tcolTest:
		return t.Name, pillNeutral, false
	case tcolStatus:
		kind := statusKind(t.Status)
		if kind == pillNeutral {
			kind = groupStatusKind(t.Status)
		}
		return t.Status, kind, true
	case tcolRun:
		return orDash(t.RunID), pillNeutral, false
	case tcolVerdict:
		return testVerdict(v.b, t)
	}
	return "", pillNeutral, false
}

func (v *batchView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}

// testVerdict is what the batch's judgment said of one test. Only a shipped
// judge's verdict is placed in a pass set; anyone else's is shown as written.
func testVerdict(b browse.Batch, t browse.BatchTest) (string, pillKind, bool) {
	switch {
	case !b.Judged:
		return "not judged", pillNeutral, false
	case t.Verdict != "":
		kind := pillNeutral
		if t.VerdictShipped {
			kind = pillBad
			if t.VerdictPassed {
				kind = pillGood
			}
		}
		return t.Judge + "@" + t.Version + " " + t.Verdict, kind, true
	case t.JudgeError != "":
		return t.JudgeStatus + ": " + t.JudgeError, pillBad, false
	default:
		return orDash(t.JudgeStatus), pillNeutral, false
	}
}

// groupStatusKind picks the colours for a batch's or campaign's own status,
// which unlike a run's is a summary of its parts.
func groupStatusKind(status string) pillKind {
	switch status {
	case "passed":
		return pillGood
	case "failed", "interrupted", campaign.StatusVerdictFail, campaign.StatusInfraFail, browse.Unreadable:
		return pillBad
	case "running":
		return pillActive
	default:
		return pillNeutral
	}
}

func showMessage(l *widget.Label, text string) {
	l.SetText(text)
	l.Show()
}
