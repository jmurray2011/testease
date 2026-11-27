package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/record"
)

// liveEvery is how often a live pane looks at the record again. A step's
// output reaches its file as the probe prints it, so this is the whole delay
// between the target saying something and the screen showing it.
const liveEvery = time.Second

// liveLimit is how much of the end of a running step's output is on screen.
// The pane follows what is happening now; the whole file is in the record.
const liveLimit = 128 << 10

// liveStep is a step whose record says it started and does not yet say how it
// ended: the step a run is in the middle of.
type liveStep struct {
	// Cell is the matrix cell the step belongs to, or "" for a run without
	// cells.
	Cell    string
	Seq     int
	Label   string
	Host    string
	Started time.Time
	// Out and Err are the step's output files, once the runner has made them.
	Out, Err string
}

// name is the step as the pane's picker shows it.
func (s liveStep) name() string {
	n := fmt.Sprintf("%d · %s", s.Seq, s.Label)
	if s.Cell != "" {
		n = s.Cell + " · " + n
	}
	return n
}

// liveEvent is the part of an event a live pane reads.
type liveEvent struct {
	Kind record.Kind     `json:"kind"`
	TS   time.Time       `json:"ts"`
	Step int             `json:"step"`
	Data json.RawMessage `json:"data"`
}

// runningSteps is every step of the record at dir that has started and not
// finished, in the run itself and in each of its cells, the most recently
// started first. It reads only the record, so a run started anywhere - this
// app, a terminal, a CI job writing to a shared folder - can be watched.
func runningSteps(dir string) []liveStep {
	steps := runningIn(dir, "")
	if entries, err := os.ReadDir(filepath.Join(dir, record.CellsDir)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				steps = append(steps, runningIn(filepath.Join(dir, record.CellsDir, e.Name()), e.Name())...)
			}
		}
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Started.After(steps[j].Started) })
	return steps
}

// runningIn reads one events file for steps still in flight.
func runningIn(dir, cell string) []liveStep {
	f, err := os.Open(filepath.Join(dir, record.EventsFile))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	open := map[int]liveStep{}
	sc := bufio.NewScanner(f)
	// An event line can carry a long argument list or claim.
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var e liveEvent
		// The last line can be half written while the runner writes it; it
		// is read whole on the next look.
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Kind {
		case record.KindStepStarted:
			var d struct {
				Label string `json:"label"`
				Host  string `json:"host"`
			}
			_ = json.Unmarshal(e.Data, &d)
			open[e.Step] = liveStep{Cell: cell, Seq: e.Step, Label: d.Label, Host: d.Host, Started: e.TS}
		case record.KindOperation:
			delete(open, e.Step)
		case record.KindRunFinished:
			return nil
		}
	}
	steps := make([]liveStep, 0, len(open))
	for _, s := range open {
		s.Out = stepFile(dir, s.Seq, s.Label, ".out")
		s.Err = stepFile(dir, s.Seq, s.Label, ".err")
		steps = append(steps, s)
	}
	return steps
}

// stepFile finds the output file the runner is writing for a step. A resumed
// cell writes a second attempt beside the first as name.2.out and so on, so
// the newest of those is the one being written now.
func stepFile(dir string, seq int, label, ext string) string {
	out, _ := record.StepPaths(seq, label)
	base := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(out, ".out")))
	matches, _ := filepath.Glob(globEscape(base) + "*" + ext)
	best, bestTime := "", time.Time{}
	for _, m := range matches {
		middle := strings.TrimSuffix(strings.TrimPrefix(m, base), ext)
		if middle != "" && !isAttempt(middle) {
			continue // another step whose label starts the same way
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = m, info.ModTime()
		}
	}
	return best
}

// isAttempt says whether s is ".N", the mark a later attempt's file carries.
func isAttempt(s string) bool {
	if len(s) < 2 || s[0] != '.' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// globEscape keeps a path's own brackets and stars from being read as a
// pattern.
func globEscape(p string) string {
	r := strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]")
	return r.Replace(p)
}

// liveView follows the step a run is in the middle of: its output as it is
// written, and which step it is. It reads the record and nothing else.
type liveView struct {
	mu      sync.Mutex
	dir     string
	stopped bool
	ticking bool
	// onUI runs a look on the app's goroutine; tests run it inline.
	onUI func(func())

	heading   *widget.Label
	picker    *widget.Select
	stdout    *widget.TextGrid
	stderr    *widget.TextGrid
	outScroll *container.Scroll
	errScroll *container.Scroll
	tabs      *container.AppTabs
	content   fyne.CanvasObject

	steps []liveStep
	// chosen is the step the person picked, by name; "" follows whichever
	// step started last.
	chosen          string
	shownOut        string
	shownErr        string
	lastOut, lastEr fileMark
	// last is the step shown most recently, kept on screen after it ends so
	// its final lines stay readable between steps.
	last *liveStep
}

// fileMark is enough of a file's state to tell that it changed.
type fileMark struct {
	path string
	size int64
	mod  time.Time
}

func newLiveView() *liveView {
	v := &liveView{onUI: fyne.Do}
	v.heading = widget.NewLabel("Waiting for the run to start a step…")
	v.heading.Truncation = fyne.TextTruncateEllipsis
	v.picker = widget.NewSelect(nil, func(name string) {
		v.mu.Lock()
		if len(v.steps) > 0 && name == v.steps[0].name() {
			name = "" // the newest step: keep following
		}
		v.chosen = name
		v.mu.Unlock()
		v.update()
	})
	v.picker.Hide()
	v.stdout = widget.NewTextGrid()
	v.stdout.Scroll = fyne.ScrollNone
	v.stderr = widget.NewTextGrid()
	v.stderr.Scroll = fyne.ScrollNone
	v.outScroll = container.NewScroll(container.NewPadded(v.stdout))
	v.errScroll = container.NewScroll(container.NewPadded(v.stderr))
	v.tabs = container.NewAppTabs(
		container.NewTabItem("stdout", v.outScroll),
		container.NewTabItem("stderr", v.errScroll),
	)
	v.content = container.NewBorder(
		container.NewBorder(nil, nil, nil, v.picker, v.heading), nil, nil, nil, v.tabs,
	)
	return v
}

// watch starts following the record at dir. It is safe to call again with
// the same dir.
func (v *liveView) watch(dir string) {
	v.mu.Lock()
	if v.dir == dir && !v.stopped {
		v.mu.Unlock()
		return
	}
	v.dir, v.stopped, v.ticking = dir, false, true
	v.mu.Unlock()
	v.update()
	go func() {
		t := time.NewTicker(liveEvery)
		defer t.Stop()
		for range t.C {
			v.mu.Lock()
			stop := v.stopped
			v.mu.Unlock()
			if stop {
				break
			}
			v.onUI(v.update)
			// A record whose writer has let go will not change again: one
			// last look has shown its end.
			if held, err := record.WriterLocked(dir); err == nil && !held {
				break
			}
		}
		v.mu.Lock()
		v.ticking = false
		v.mu.Unlock()
	}()
}

// stop ends the following, for a screen the person has left.
func (v *liveView) stop() {
	v.mu.Lock()
	v.stopped = true
	v.mu.Unlock()
}

// update looks at the record again and shows what changed. It runs on the UI
// goroutine.
func (v *liveView) update() {
	v.mu.Lock()
	dir := v.dir
	v.mu.Unlock()
	if dir == "" {
		return
	}
	steps := runningSteps(dir)
	v.mu.Lock()
	v.steps = steps
	chosen := v.chosen
	v.mu.Unlock()

	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.name()
	}
	if len(steps) > 1 {
		v.picker.Options = names
		v.picker.Show()
	} else {
		v.picker.Hide()
	}

	var cur *liveStep
	for i := range steps {
		if chosen == "" || steps[i].name() == chosen {
			cur = &steps[i]
			break
		}
	}
	if cur == nil && len(steps) > 0 {
		cur = &steps[0]
	}
	if len(steps) > 1 && cur != nil && v.picker.Selected != cur.name() {
		v.picker.SetSelected(cur.name())
	}

	running := cur != nil
	if cur == nil {
		cur = v.last
	} else {
		c := *cur
		v.last = &c
	}
	if cur == nil {
		if held, err := record.WriterLocked(dir); err == nil && !held {
			v.heading.SetText("The run is not running a step now.")
		}
		return
	}

	head := cur.name()
	if cur.Host != "" {
		head += " · on " + cur.Host
	}
	if running {
		head = "Now: " + head + " · " + time.Since(cur.Started).Round(time.Second).String()
	} else {
		head = "Last: " + head + " · finished"
	}
	v.heading.SetText(head)

	v.follow(cur.Out, &v.lastOut, &v.shownOut, v.stdout, v.outScroll)
	v.follow(cur.Err, &v.lastEr, &v.shownErr, v.stderr, v.errScroll)
}

// follow shows the end of one output file if it changed since the last look,
// and keeps the view at the bottom if that is where the person left it.
func (v *liveView) follow(path string, mark *fileMark, shown *string, grid *widget.TextGrid, scroll *container.Scroll) {
	var now fileMark
	now.path = path
	if path != "" {
		if info, err := os.Stat(path); err == nil {
			now.size, now.mod = info.Size(), info.ModTime()
		}
	}
	if now == *mark {
		return
	}
	*mark = now
	var text string
	switch {
	case path == "":
		text = "(nothing written yet)"
	case now.size == 0:
		text = "(nothing written yet)"
	default:
		text = readTail(path, liveLimit)
	}
	if text == *shown {
		return
	}
	atBottom := scroll.Offset.Y >= scroll.Content.MinSize().Height-scroll.Size().Height-8
	*shown = text
	grid.SetText(text)
	if atBottom {
		scroll.ScrollToBottom()
	}
}

// liveScreen is a running record's live pane on a screen of its own, for a
// run this app did not start: one from a terminal, a batch or a campaign.
type liveScreen struct {
	live    *liveView
	content fyne.CanvasObject
}

func newLiveScreen(dir, backLabel string, back func()) *liveScreen {
	v := &liveScreen{live: newLiveView()}
	top := backHeader(backLabel, back, filepath.Base(dir)+" · live", container.NewCenter(newPill(statusRunning, pillActive)), openFolderButton(dir))
	v.content = container.NewBorder(
		container.NewVBox(container.NewPadded(top), widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), note("This follows the record as the run writes it, so it works for a run started anywhere that writes to this folder. To stop the run, stop it where it was started.")),
		nil, nil, container.NewPadded(v.live.content),
	)
	v.live.watch(dir)
	return v
}
