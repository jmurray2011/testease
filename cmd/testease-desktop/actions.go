package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/jmurray2011/testease/internal/targetcatalog"
)

// tools is what starting a run needs beyond the runs directory: where
// scenarios are, the testease binary to call, and the target catalog whose
// names a run can pick from.
type tools struct {
	scenarioRoot, scenarioFrom string
	testease, testeaseFrom     string
	catalog                    string
	rememberRoot               func(string)
}

func (s *shell) useTools(t tools) { s.tools = t }

// onUI runs f on the app's goroutine: a job reports from its own.
func (s *shell) onUI(f func()) {
	if s.uiCall != nil {
		s.uiCall(f)
		return
	}
	fyne.Do(f)
}

func (s *shell) showScenarios() {
	s.highlight(sectionScenarios)
	v := newScenariosView(s.tools.scenarioRoot, s.tools.scenarioFrom, s.openScenario)
	v.onChoose = s.chooseScenarioRoot
	s.show(v, v.content)
}

func (s *shell) chooseScenarioRoot() {
	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}
		if s.tools.rememberRoot != nil {
			s.tools.rememberRoot(uri.Path())
		}
		s.tools.scenarioRoot, s.tools.scenarioFrom = uri.Path(), "chosen here"
		s.showScenarios()
	}, s.window)
	d.Show()
}

func (s *shell) openScenario(name string) {
	v, err := newScenarioView(scenarioContext{
		runsDir: s.dir, root: s.tools.scenarioRoot, name: name,
		targets: catalogNames(s.tools.catalog), start: s.startJob,
	}, s.showScenarios)
	if err != nil {
		dialog.ShowError(err, s.window)
		return
	}
	s.highlight(sectionScenarios)
	s.show(v, v.content)
}

// catalogNames is the targets a catalog names, for picking one. A catalog
// that will not load offers none; the run will say why if it is named.
func catalogNames(path string) []string {
	if path == "" {
		return nil
	}
	c, err := targetcatalog.Load(path)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(c.Targets))
	for _, t := range c.Targets {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// startJob starts a testease command and shows it.
func (s *shell) startJob(title string, args []string) {
	if s.dir == "" {
		dialog.ShowError(errors.New("choose the runs directory first: a run writes its record there"), s.window)
		return
	}
	var v *jobView
	v = newJobView(title, args, s.dir, s.showActivity, func(rec string) {
		s.openRecord(rec, "Activity", func() { s.highlight(sectionActivity); s.show(v, v.content) })
	})
	s.jobs = append(s.jobs, v)
	s.highlight(sectionActivity)
	s.show(v, v.content)

	if s.tools.testease == "" {
		v.fail(errors.New("no testease binary found: put testease beside this app or on PATH, or start this app with --testease <path>"))
		return
	}
	j, err := startJob(s.tools.testease, args, os.Environ(), func() { s.onUI(v.refresh) })
	if err != nil {
		v.fail(err)
		return
	}
	v.attach(j)
}

func (s *shell) showActivity() {
	s.highlight(sectionActivity)
	v := newActivityView(func() []*jobView { return s.jobs }, func(jv *jobView) {
		s.highlight(sectionActivity)
		s.show(jv, jv.content)
	})
	s.show(v, v.content)
}

// running is the jobs still going.
func (s *shell) running() []*jobView {
	var out []*jobView
	for _, jv := range s.jobs {
		if jv.j != nil && !jv.j.state().Done {
			out = append(out, jv)
		}
	}
	return out
}

// stopWait is how long quitting waits for stopped runs to close their records.
const stopWait = 2 * time.Minute

// quit closes the window. A run still going is stopped first, the same way
// Stop does, so its record says it was interrupted rather than nothing at
// all; quitting does not wait past stopWait for one that will not stop.
func (s *shell) quit() {
	running := s.running()
	if len(running) == 0 {
		s.window.Close()
		return
	}
	msg := fmt.Sprintf("%s still going. Quitting stops %s first, so each record says it was interrupted, then closes the app.",
		plural(len(running), "run or check is"), map[bool]string{true: "it", false: "them"}[len(running) == 1])
	dialog.ShowConfirm("Stop and quit?", msg, func(ok bool) {
		if !ok {
			return
		}
		for _, jv := range running {
			_ = jv.j.stop()
		}
		go func() {
			deadline := time.After(stopWait)
			for _, jv := range running {
				select {
				case <-jv.j.exited:
				case <-deadline:
				}
			}
			fyne.Do(s.window.Close)
		}()
	}, s.window)
}
