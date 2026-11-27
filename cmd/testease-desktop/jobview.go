package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/record"
)

// The job screen's columns.
const (
	jcolStep = iota
	jcolStatus
	jcolTime
)

var (
	jobTitles = []string{"Step", "Status", "Time"}
	jobWidths = []float32{520, 130, 100}
)

// jobView is one testease command started from this app, live.
type jobView struct {
	title   string
	args    []string
	runsDir string
	isRun   bool
	started time.Time
	j       *job
	s       jobState

	status *pill
	steps  *widget.Table
	log    *widget.TextGrid
	// live follows the step the run is in, from its record; outTabs holds
	// it beside testease's own output.
	live    *liveView
	outTabs *container.AppTabs
	stop    *widget.Button
	open    *widget.Button
	targets *widget.Button
	content fyne.CanvasObject
	openRun func(dir string)
	// record is set for a job about a record already on disk, like a judge.
	record   string
	lastLogN int
}

func newJobView(title string, args []string, runsDir string, back, showTargets func(), openRun func(string)) *jobView {
	v := &jobView{title: title, args: args, runsDir: runsDir, started: time.Now(), openRun: openRun}
	// Only a scenario run reports its steps; anything else is its output.
	v.isRun = command(args) == "run"
	v.status = newPill("starting", pillActive)
	v.stop = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), v.stopOrKill)
	v.open = widget.NewButtonWithIcon("Open record", theme.NavigateNextIcon(), v.openRecord)
	v.open.Disable()
	// A run that did not finish cleanly keeps its targets; this is where
	// they are seen and cleaned up.
	v.targets = widget.NewButtonWithIcon("Kept targets…", theme.ComputerIcon(), showTargets)
	v.targets.Hide()
	top := backHeader("Activity", back, title, container.NewCenter(v.status), v.stop, v.open, v.targets)
	command := note("testease " + strings.Join(args, " "))

	v.steps = listing(func() int { return len(v.s.Steps) }, jobTitles, jobWidths, -1, v.cell, nil)
	v.log = widget.NewTextGrid()
	v.log.Scroll = fyne.ScrollNone
	logPane := container.NewBorder(widget.NewLabelWithStyle("Output", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil,
		container.NewScroll(container.NewPadded(v.log)))

	var body fyne.CanvasObject = logPane
	if v.isRun {
		// The step's own output is what a person watching a run wants; what
		// testease says about the run is a tab away.
		v.live = newLiveView()
		v.outTabs = container.NewAppTabs(
			container.NewTabItem("Live step output", v.live.content),
			container.NewTabItem("testease", container.NewScroll(container.NewPadded(v.log))),
		)
		split := container.NewVSplit(v.steps, v.outTabs)
		split.Offset = 0.55
		body = split
	}
	v.content = container.NewBorder(
		container.NewVBox(container.NewPadded(container.NewVBox(top, command)), widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), note("Stop interrupts the run the way Ctrl+C does: it finishes what is in flight and closes its record as interrupted. Kill is for a run that will not stop, and leaves its record unfinished.")),
		nil, nil, body,
	)
	return v
}

// attach starts showing a job.
func (v *jobView) attach(j *job) {
	v.j = j
	v.refresh()
}

// fail shows a job that never started.
func (v *jobView) fail(err error) {
	v.s = jobState{Done: true, Exit: -1, Log: err.Error()}
	v.render()
}

func (v *jobView) refresh() {
	if v.j != nil {
		v.s = v.j.state()
	}
	v.render()
}

func (v *jobView) render() {
	text, kind := v.statusText()
	v.status.Set(text, kind)
	switch {
	case v.s.Done:
		v.stop.SetText("Stop")
		v.stop.Importance = widget.MediumImportance
		v.stop.Disable()
	case v.s.Stopping:
		v.stop.SetText("Kill")
		v.stop.Importance = widget.DangerImportance
		v.stop.Refresh()
	}
	if v.s.Done && v.recordDir() != "" {
		v.open.Enable()
	}
	if v.keptTargets() {
		v.targets.Show()
	}
	if v.live != nil {
		if dir := v.recordDir(); dir != "" {
			v.live.watch(dir)
		} else if v.s.Done && v.outTabs.SelectedIndex() == 0 {
			// It ended without a record: why is in testease's output.
			v.outTabs.SelectIndex(1)
		}
	}
	out := v.s.Log
	if !v.isRun && v.s.Stdout != "" {
		out = v.s.Stdout + out
	}
	if len(out) != v.lastLogN {
		v.lastLogN = len(out)
		v.log.SetText(out)
	}
	v.steps.Refresh()
}

// statusText is the job's state in a word or two: what the run says of
// itself once it has said it, and the exit code of a command that is not one.
func (v *jobView) statusText() (string, pillKind) {
	switch {
	case !v.s.Done && v.s.Stopping:
		return "stopping", pillActive
	case !v.s.Done:
		return "running", pillActive
	case v.isRun && v.s.Status != "":
		return v.s.Status, statusKind(v.s.Status)
	case v.s.Exit == 0:
		return "exit 0", pillGood
	default:
		return fmt.Sprintf("exit %d", v.s.Exit), pillBad
	}
}

func (v *jobView) stopOrKill() {
	if v.j == nil {
		return
	}
	if v.s.Stopping {
		_ = v.j.kill()
		return
	}
	if err := v.j.stop(); err != nil {
		v.s.Log += "could not stop the run: " + err.Error() + "\n"
		v.render()
	}
}

// keptTargets is whether the job may have left targets standing: a run that
// says it did not measure cleanly, or a batch or campaign of runs that did
// not exit cleanly.
func (v *jobView) keptTargets() bool {
	switch {
	case !v.s.Done || !startsTargets(v.args):
		return false
	case v.isRun:
		return v.s.Status != "" && v.s.Status != string(record.StatusMeasured)
	default:
		return v.s.Exit != 0
	}
}

// recordDir is the record the job is about: the one a judge job read, or the
// one a run wrote, once it is on disk.
func (v *jobView) recordDir() string {
	if v.record != "" {
		return v.record
	}
	if v.s.RunID == "" || !filepath.IsLocal(v.s.RunID) {
		return ""
	}
	dir := filepath.Join(v.runsDir, v.s.RunID)
	if _, err := os.Stat(filepath.Join(dir, record.EventsFile)); err != nil {
		return ""
	}
	return dir
}

func (v *jobView) openRecord() {
	if dir := v.recordDir(); dir != "" {
		v.openRun(dir)
	}
}

func (v *jobView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.s.Steps) {
		return "", pillNeutral, false
	}
	s := v.s.Steps[row]
	switch col {
	case jcolStep:
		label := fmt.Sprintf("%d · %s", s.Seq, s.Label)
		if s.Cell != "" {
			label = s.Cell + " · " + label
		}
		return label, pillNeutral, false
	case jcolStatus:
		switch {
		case s.Running:
			return "running", pillActive, true
		case s.Status == "":
			return "no outcome", pillNeutral, true
		default:
			return s.Status, statusKind(s.Status), true
		}
	case jcolTime:
		if s.Running || s.Seconds == 0 {
			return "", pillNeutral, false
		}
		return fmt.Sprintf("%.1f s", s.Seconds), pillNeutral, false
	}
	return "", pillNeutral, false
}

func (v *jobView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}

// The Activity screen's columns.
const (
	acolStarted = iota
	acolCommand
	acolState
)

var (
	activityListTitles = []string{"Started", "Command", "State"}
	activityListWidths = []float32{110, 520, 140}
)

// activityView lists the commands started from this app since it opened.
type activityView struct {
	jobs    func() []*jobView
	onOpen  func(*jobView)
	message *widget.Label
	table   *widget.Table
	content fyne.CanvasObject
}

func newActivityView(jobs func() []*jobView, onOpen func(*jobView)) *activityView {
	v := &activityView{jobs: jobs, onOpen: onOpen, message: message()}
	v.table = listing(func() int { return len(v.jobs()) }, activityListTitles, activityListWidths, -1, v.cell,
		func(row int) { v.onOpen(v.jobs()[len(v.jobs())-1-row]) })
	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Activity", nil), widget.NewSeparator(), v.message),
		container.NewVBox(widget.NewSeparator(), note("Everything started here since the app opened, newest first. Every run's record is in Runs as well.")),
		nil, nil, v.table,
	)
	v.reload()
	return v
}

func (v *activityView) reload() {
	if len(v.jobs()) == 0 {
		showMessage(v.message, "Nothing started yet. Open a scenario to run it or check it with doctor.")
	} else {
		v.message.Hide()
	}
	v.table.Refresh()
}

func (v *activityView) cell(row, col int) (string, pillKind, bool) {
	jobs := v.jobs()
	if row < 0 || row >= len(jobs) {
		return "", pillNeutral, false
	}
	jv := jobs[len(jobs)-1-row]
	switch col {
	case acolStarted:
		return jv.started.UTC().Format("15:04:05") + " UTC", pillNeutral, false
	case acolCommand:
		return jv.title, pillNeutral, false
	case acolState:
		text, kind := jv.statusText()
		return text, kind, true
	}
	return "", pillNeutral, false
}

func (v *activityView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}
