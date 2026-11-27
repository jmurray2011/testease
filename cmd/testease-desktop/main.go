// Command testease-desktop is a desktop app for browsing testease records.
//
// It is a separate binary from testease so that the CLI stays a static,
// CGO-free build: this one needs a C toolchain and a GPU driver, the CLI never
// should. It reads records through the same packages the CLI does and, in this
// slice, changes nothing.
package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
)

// appID names the app to the OS: where it keeps its one preference, and how
// the desktop groups its windows.
const appID = "io.github.jmurray2011.testease"

// refreshEvery is how often the Runs screen re-reads the runs directory, so a
// run that finishes or a verdict that lands shows up without a click.
const refreshEvery = 5 * time.Second

// openingSize is the window the app opens at, and sidebarWidth the sections
// column's share of it. Every screen is laid out to fit beside the sections at
// this size.
var openingSize = fyne.NewSize(1380, 820)

const sidebarWidth = 208

func main() {
	runsDir := flag.String("runs-dir", "", "where records are written (default $TESTEASE_RUNS_DIR, then the folder last chosen here)")
	scenarioRoot := flag.String("scenario-root", "", "where scenarios are (default $TESTEASE_SCENARIO_ROOT, then the folder last chosen here)")
	testease := flag.String("testease", "", "the testease binary runs are started with (default the one beside this app, then the one on PATH)")
	flag.Parse()

	a := app.NewWithID(appID)
	a.Settings().SetTheme(appTheme{})
	a.SetIcon(appIcon)

	dir, from := resolveRunsDir(*runsDir, os.Getenv("TESTEASE_RUNS_DIR"), a.Preferences().String(prefRunsDir))
	w := a.NewWindow("testease")
	s := newShell(w, dir, from, func(chosen string) { a.Preferences().SetString(prefRunsDir, chosen) })
	root, rootFrom := resolvePath(*scenarioRoot, "--scenario-root", os.Getenv("TESTEASE_SCENARIO_ROOT"), "TESTEASE_SCENARIO_ROOT", a.Preferences().String(prefScenarioRoot))
	self, _ := os.Executable()
	exe, exeFrom := findTestease(*testease, self, exec.LookPath)
	s.useTools(tools{
		scenarioRoot: root, scenarioFrom: rootFrom, testease: exe, testeaseFrom: exeFrom,
		catalog:      os.Getenv("TESTEASE_TARGET_CATALOG"),
		rememberRoot: func(chosen string) { a.Preferences().SetString(prefScenarioRoot, chosen) },
	})
	w.SetCloseIntercept(s.quit)
	w.SetContent(s.content)
	// Shortcuts go on the canvas once it holds the content: added earlier,
	// the window paints nothing on Windows.
	s.addShortcuts()
	w.Resize(openingSize)

	go func() {
		for range time.Tick(refreshEvery) {
			fyne.Do(s.tick)
		}
	}()
	w.ShowAndRun()
}

// navSection is one of the sidebar's sections.
type navSection int

const (
	sectionHome navSection = iota
	sectionRuns
	sectionBatches
	sectionCampaigns
	sectionScenarios
	sectionActivity
	sectionTargets
	sectionSettings
	sections
)

var sectionNames = [sections]string{"Home", "Runs", "Batches", "Campaigns", "Scenarios", "Activity", "Targets", "Settings"}

// shell is the window: sections on the left, the current screen on the right.
type shell struct {
	window   fyne.Window
	remember func(dir string)

	dir, from string
	section   navSection
	home      *homeView
	runs      *runsView
	batches   *batchesView
	campaigns *campaignsView
	// current is the screen showing now, so the refresh knows what to
	// re-read and back knows where it is.
	current any
	// shown is the section each recent screen was first shown under, so a
	// screen gone back to highlights its own section, not the last one's.
	shown []shownScreen

	tools tools
	jobs  []*jobView
	// uiCall stands in for fyne.Do in tests, whose driver runs it inline on
	// whatever goroutine calls it.
	uiCall func(func())

	// background is whether the app is behind other windows, where a job
	// that ends is worth a desktop notification; notify sends one.
	background bool
	notify     func(title, body string)

	buttons [sections]*widget.Button
	where   *widget.Label
	body    *fyne.Container
	content fyne.CanvasObject
}

func newShell(w fyne.Window, dir, from string, remember func(string)) *shell {
	s := &shell{window: w, remember: remember, dir: dir, from: from}
	s.notify = func(title, body string) {
		fyne.CurrentApp().SendNotification(fyne.NewNotification(title, body))
	}
	life := fyne.CurrentApp().Lifecycle()
	life.SetOnExitedForeground(func() { s.background = true })
	life.SetOnEnteredForeground(func() { s.background = false })

	name := widget.NewLabelWithStyle("testease", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	logo := canvas.NewImageFromResource(logoIcon)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(22, 22))
	nav := container.NewVBox(container.NewHBox(container.NewPadded(logo), name))
	shows := s.sectionShows()
	icons := sectionIcons()
	for i := range sections {
		b := widget.NewButtonWithIcon(sectionNames[i], icons[i], shows[i])
		b.Alignment = widget.ButtonAlignLeading
		s.buttons[i] = b
		nav.Add(b)
	}

	caption := widget.NewLabel("Runs directory")
	caption.Importance = widget.LowImportance
	s.where = widget.NewLabel("")
	s.where.Truncation = fyne.TextTruncateEllipsis
	change := widget.NewButtonWithIcon("Choose…", theme.FolderOpenIcon(), s.chooseDir)

	navBox := container.NewBorder(
		nav,
		container.NewVBox(widget.NewSeparator(), caption, s.where, change),
		nil, nil,
	)
	navBackground := canvas.NewRectangle(theme.Color(colorNav))
	navPanel := container.NewStack(navBackground, container.NewPadded(navBox))
	fyne.CurrentApp().Settings().AddListener(func(fyne.Settings) {
		navBackground.FillColor = theme.Color(colorNav)
		navBackground.Refresh()
	})

	s.body = container.NewStack()
	sidebar := container.NewHBox(navPanelSized(navPanel), widget.NewSeparator())
	s.content = container.NewBorder(nil, nil, sidebar, nil, s.body)
	s.setDir(dir, from)
	return s
}

// navPanelSized keeps the sections column a fixed width however wide the
// window is.
func navPanelSized(o fyne.CanvasObject) fyne.CanvasObject {
	return container.New(&fixedWidth{width: sidebarWidth}, o)
}

type fixedWidth struct{ width float32 }

func (f *fixedWidth) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func (f *fixedWidth) MinSize(objects []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	for _, o := range objects {
		if m := o.MinSize(); m.Height > h {
			h = m.Height
		}
	}
	return fyne.NewSize(f.width, h)
}

func (s *shell) setDir(dir, from string) {
	s.dir, s.from = dir, from
	if dir == "" {
		s.where.SetText("not chosen")
	} else {
		// Where the folder was taken from is on the Settings screen; the
		// sidebar has room for the folder and no more.
		name, in := sidebarPath(dir)
		s.where.SetText(strings.TrimSpace(name + "\n" + in))
	}
	s.home = newHomeView(dir, func(rec string) { s.openRecord(rec, "Home", s.showHome) }, s.showScenarioRuns)
	s.runs = newRunsView(dir, func(rec string) { s.openRecord(rec, "Runs", s.showRuns) })
	s.runs.compare.set(s.chooseRunsToCompare)
	s.batches = newBatchesView(dir, s.openBatch)
	s.batches.run.set(s.chooseBatchFile)
	s.campaigns = newCampaignsView(dir, func(c browse.Campaign) { s.openCampaign(c.ID) })
	s.campaigns.run.set(s.chooseCampaign)
	s.shown = nil
	s.showHome()
}

// highlight marks the section a screen belongs to.
func (s *shell) highlight(sec navSection) {
	s.section = sec
	for i, b := range s.buttons {
		if navSection(i) == sec {
			b.Importance = widget.HighImportance
		} else {
			b.Importance = widget.LowImportance
		}
		b.Refresh()
	}
}

func (s *shell) showHome() {
	s.highlight(sectionHome)
	s.home.reload()
	s.show(s.home, s.home.content)
}

// showScenarioRuns lists one scenario's runs.
func (s *shell) showScenarioRuns(name string) {
	s.runs.onlyScenario(name)
	s.showRuns()
}

func (s *shell) showRuns() {
	s.highlight(sectionRuns)
	s.runs.reload()
	s.show(s.runs, s.runs.content)
}

func (s *shell) showBatches() {
	s.highlight(sectionBatches)
	s.batches.reload()
	s.show(s.batches, s.batches.content)
}

func (s *shell) showCampaigns() {
	s.highlight(sectionCampaigns)
	s.campaigns.reload()
	s.show(s.campaigns, s.campaigns.content)
}

func (s *shell) showSettings() {
	s.highlight(sectionSettings)
	v := newSettingsView(s.dir, s.from, os.Getenv)
	v.addTools(s.tools)
	v.showVersions(thisVersion(), execCall(s.tools.testease))
	s.show(v, v.content)
}

func (s *shell) openBatch(b browse.Batch) {
	var v *batchView
	back := func() { s.show(v, v.content) }
	v = newBatchView(s.dir, b, s.showBatches, func(rec string) { s.openRecord(rec, "Batch", back) })
	v.judge.set(func() { s.startJob("judge batch "+b.ID, batchJudgeArgs(s.dir, b.ID)) })
	s.highlight(sectionBatches)
	s.show(v, v.content)
}

func (s *shell) openCampaign(id string) {
	var v *campaignView
	back := func() { v.returned(); s.show(v, v.content) }
	v = newCampaignView(s.dir, id, s.showCampaigns, func(rec string) { s.openRecord(rec, "Campaign", back) })
	v.pack.set(func() { s.startJob("package "+id, packageArgs(s.dir, id)) })
	s.highlight(sectionCampaigns)
	s.show(v, v.content)
}

// backSections is the section of each screen a record or a comparison can
// say back goes to, by the name its back button carries. The sidebar
// highlights that section, so it agrees with the back button: a record
// opened from the command palette says "Runs", and is under Runs, whatever
// screen the palette was opened over. A name not here - a cell's matrix run,
// a job, a record - keeps the section showing.
var backSections = map[string]navSection{
	"Home":      sectionHome,
	"Runs":      sectionRuns,
	"Batch":     sectionBatches,
	"Campaign":  sectionCampaigns,
	"Scenarios": sectionScenarios,
	"Targets":   sectionTargets,
}

func (s *shell) highlightFrom(from string) {
	if sec, ok := backSections[from]; ok {
		s.highlight(sec)
	}
}

// openRecord opens a record, with back naming and going to wherever it was
// opened from.
func (s *shell) openRecord(dir, from string, back func()) {
	v, err := newDetailViewFrom(dir, from, back)
	if err != nil {
		dialog.ShowError(err, s.window)
		return
	}
	v.onOpenCell = func(cell string) { s.openRecord(cell, "Matrix", func() { s.show(v, v.content) }) }
	v.judge.set(func() { s.startRecordJob(dir, "judge "+filepath.Base(dir), judgeArgs(s.dir, dir)) })
	v.render.set(func() { s.chooseRender(dir) })
	v.compare.set(func() { s.chooseCompare(dir, func() { s.show(v, v.content) }) })
	v.again.set(func() { s.runAgain(v) })
	if statusOf(v.d.Run) == statusRunning {
		v.watch.set(func() {
			l := newLiveScreen(dir, "Record", func() { s.show(v, v.content) })
			s.show(l, l.content)
		})
	}
	s.highlightFrom(from)
	s.show(v, v.content)
}

// show puts a screen in the body. Fyne grows a window to whatever its content
// asks for, so a screen sits in a scroll: one that asks for more room than the
// window has scrolls, and the window keeps the size the person gave it.
//
// A screen shown again, by back, highlights the section it was first shown
// under: going back to a record from the scenario form Run again opened
// highlights the record's section, not Scenarios.
func (s *shell) show(view any, screen fyne.CanvasObject) {
	// A live screen left behind stops reading the record.
	if l, ok := s.current.(*liveScreen); ok && l != view {
		l.live.stop()
	}
	s.rememberSection(view)
	s.current = view
	s.body.Objects = []fyne.CanvasObject{container.NewScroll(screen)}
	s.body.Refresh()
}

// shownScreen is a screen and the section it was first shown under.
type shownScreen struct {
	view    any
	section navSection
}

// recentScreens is how many screens back the sidebar remembers sections for.
// Back goes a few screens at most; holding every screen ever opened would
// hold every record's output with it.
const recentScreens = 32

// rememberSection highlights the section a screen was first shown under when
// it is shown again, and otherwise notes the section it is shown under now.
func (s *shell) rememberSection(view any) {
	for i, sc := range s.shown {
		if sc.view == view {
			if sc.section != s.section {
				s.highlight(sc.section)
			}
			// Most recent last, so the oldest go first.
			s.shown = append(append(s.shown[:i:i], s.shown[i+1:]...), sc)
			return
		}
	}
	s.shown = append(s.shown, shownScreen{view: view, section: s.section})
	if len(s.shown) > recentScreens {
		s.shown = s.shown[len(s.shown)-recentScreens:]
	}
}

// tick re-reads the screen showing, so a run that finishes or a verdict that
// lands shows up without a click. A record that is open is left alone:
// re-reading it under the person reading it would move the selection they are
// looking at.
func (s *shell) tick() {
	s.countJobs()
	switch v := s.current.(type) {
	case *homeView:
		v.reload()
	case *runsView:
		v.reload()
	case *batchesView:
		v.reload()
	case *batchView:
		v.reload()
	case *campaignsView:
		v.reload()
	case *campaignView:
		v.reload()
	case *activityView:
		v.reload()
	}
}

// countJobs keeps Activity's count of running jobs current.
func (s *shell) countJobs() {
	if b := s.buttons[sectionActivity]; b.Text != activityLabel(len(s.running())) {
		b.SetText(activityLabel(len(s.running())))
	}
}

func (s *shell) chooseDir() {
	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}
		s.remember(uri.Path())
		s.setDir(uri.Path(), "chosen here")
	}, s.window)
	d.Show()
}
