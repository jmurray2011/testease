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
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// appID names the app to the OS: where it keeps its one preference, and how
// the desktop groups its windows.
const appID = "io.github.jmurray2011.testease"

// refreshEvery is how often the Runs screen re-reads the runs directory, so a
// run that finishes or a verdict that lands shows up without a click.
const refreshEvery = 5 * time.Second

func main() {
	runsDir := flag.String("runs-dir", "", "where records are written (default $TESTEASE_RUNS_DIR, then the folder last chosen here)")
	flag.Parse()

	a := app.NewWithID(appID)
	a.Settings().SetTheme(appTheme{})

	dir, from := resolveRunsDir(*runsDir, os.Getenv("TESTEASE_RUNS_DIR"), a.Preferences().String(prefRunsDir))
	w := a.NewWindow("testease")
	s := newShell(w, dir, from, func(chosen string) { a.Preferences().SetString(prefRunsDir, chosen) })
	w.SetContent(s.content)
	w.Resize(fyne.NewSize(1380, 820))

	go func() {
		for range time.Tick(refreshEvery) {
			fyne.Do(s.tick)
		}
	}()
	w.ShowAndRun()
}

// shell is the window: sections on the left, the current screen on the right.
type shell struct {
	window   fyne.Window
	remember func(dir string)

	dir, from string
	runs      *runsView
	onRuns    bool

	where   *widget.Label
	body    *fyne.Container
	content fyne.CanvasObject
}

func newShell(w fyne.Window, dir, from string, remember func(string)) *shell {
	s := &shell{window: w, remember: remember, dir: dir, from: from}

	name := widget.NewLabelWithStyle("testease", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	runsButton := widget.NewButton("Runs", s.showRuns)
	runsButton.Alignment = widget.ButtonAlignLeading
	runsButton.Importance = widget.HighImportance

	caption := widget.NewLabel("Runs directory")
	caption.Importance = widget.LowImportance
	s.where = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	s.where.Truncation = fyne.TextTruncateEllipsis
	change := widget.NewButtonWithIcon("Choose…", theme.FolderOpenIcon(), s.chooseDir)

	nav := container.NewBorder(
		container.NewVBox(name, runsButton),
		container.NewVBox(widget.NewSeparator(), caption, s.where, change),
		nil, nil,
	)
	navBackground := canvas.NewRectangle(theme.Color(colorNav))
	navPanel := container.NewStack(navBackground, container.NewPadded(nav))
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
	return container.New(&fixedWidth{width: 208}, o)
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
		s.where.SetText(shortPath(dir) + "\n(" + from + ")")
	}
	s.runs = newRunsView(dir, s.openRecord)
	s.showRuns()
}

func (s *shell) showRuns() {
	s.onRuns = true
	s.runs.reload()
	s.body.Objects = []fyne.CanvasObject{s.runs.content}
	s.body.Refresh()
}

func (s *shell) openRecord(dir string) {
	v, err := newDetailView(dir, s.showRuns)
	if err != nil {
		dialog.ShowError(err, s.window)
		return
	}
	s.onRuns = false
	s.body.Objects = []fyne.CanvasObject{v.content}
	s.body.Refresh()
}

// tick refreshes the Runs screen while it is showing. A record that is open
// is left alone: re-reading it under the person reading it would move the
// selection they are looking at.
func (s *shell) tick() {
	if s.onRuns {
		s.runs.reload()
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
