package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

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
	d.Resize(pickerSize)
	d.Show()
}

func (s *shell) openScenario(name string) {
	v, err := newScenarioView(scenarioContext{
		runsDir: s.dir, root: s.tools.scenarioRoot, name: name,
		targets: catalogNames(s.tools.catalog), start: func(title string, args []string) { s.startJob(title, args) },
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
func (s *shell) startJob(title string, args []string) *jobView {
	if s.dir == "" {
		dialog.ShowError(errors.New("choose the runs directory first: a run writes its record there"), s.window)
		return nil
	}
	var v *jobView
	v = newJobView(title, args, s.dir, s.showActivity, s.showTargets, func(rec string) {
		s.openRecord(rec, title, func() { s.highlight(sectionActivity); s.show(v, v.content) })
	})
	s.jobs = append(s.jobs, v)
	s.highlight(sectionActivity)
	s.show(v, v.content)

	if s.tools.testease == "" {
		v.fail(errors.New("no testease binary found: put testease beside this app or on PATH, or start this app with --testease <path>"))
		return v
	}
	j, err := startJob(s.tools.testease, args, os.Environ(), func() { s.onUI(v.refresh) })
	if err != nil {
		v.fail(err)
		return v
	}
	v.attach(j)
	return v
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

func (s *shell) showTargets() {
	s.highlight(sectionTargets)
	var v *targetsView
	v = newTargetsView(s.dir, execCall(s.tools.testease), func(rec string) {
		s.openRecord(rec, "Targets", func() { s.highlight(sectionTargets); s.show(v, v.content) })
	})
	v.ask = func(title, msg string, yes func()) {
		dialog.ShowConfirm(title, msg, func(ok bool) {
			if ok {
				yes()
			}
		}, s.window)
	}
	s.show(v, v.content)
	v.reload()
}

// pickerSize is big enough to find a file in; Fyne's own is small.
var pickerSize = fyne.NewSize(900, 640)

// chooseBatchFile asks for a batch file and runs it.
func (s *shell) chooseBatchFile() {
	d := dialog.NewFileOpen(func(f fyne.URIReadCloser, err error) {
		if err != nil || f == nil {
			return
		}
		path := pickedPath(f.URI().Path())
		_ = f.Close()
		rememberFolder(prefBatchFolder, path)
		s.runBatch(path)
	}, s.window)
	d.SetFilter(storage.NewExtensionFileFilter([]string{".yaml", ".yml"}))
	if l := startFolder(prefBatchFolder); l != nil {
		d.SetLocation(l)
	}
	d.Resize(pickerSize)
	d.Show()
}

func (s *shell) runBatch(file string) {
	s.startJob("batch "+filepath.Base(file), batchRunArgs(s.dir, s.tools.scenarioRoot, file))
}

// chooseCampaign asks for a campaign's folder, then for any overrides, and
// runs it. Nothing typed here is remembered: an override changes what is
// measured, so each run says it again.
func (s *shell) chooseCampaign() {
	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}
		dir := pickedPath(uri.Path())
		rememberFolder(prefCampaignFolder, dir)
		extra := overridesEntry()
		form := dialog.NewForm("Run campaign "+filepath.Base(dir), "Run", "Cancel", []*widget.FormItem{
			widget.NewFormItem("Folder", note(dir)),
			{Text: "Overrides", Widget: extra, HintText: "One flag a line, passed as typed. Leave empty to run the campaign as written."},
		}, func(ok bool) {
			if !ok {
				return
			}
			if err := s.runCampaign(dir, extra.Text); err != nil {
				dialog.ShowError(err, s.window)
			}
		}, s.window)
		form.Resize(fyne.NewSize(560, 320))
		form.Show()
	}, s.window)
	if l := startFolder(prefCampaignFolder); l != nil {
		d.SetLocation(l)
	}
	d.Resize(pickerSize)
	d.Show()
}

func (s *shell) runCampaign(dir, extra string) error {
	args, err := campaignRunArgs(s.dir, s.tools.scenarioRoot, dir, extra)
	if err != nil {
		return err
	}
	s.startJob("campaign "+filepath.Base(dir), args)
	return nil
}

// chooseRender asks which view to render a record in, and renders it.
func (s *shell) chooseRender(dir string) {
	choices := renderChoices(dir)
	pick := widget.NewSelect(choices, nil)
	pick.SetSelectedIndex(0)
	form := dialog.NewForm("Render "+filepath.Base(dir), "Render", "Cancel", []*widget.FormItem{
		{Text: "View", Widget: pick, HintText: "Views marked recorded are the ones the scenario named."},
	}, func(ok bool) {
		if ok && pick.Selected != "" {
			s.renderRecord(dir, pick.Selected)
		}
	}, s.window)
	form.Resize(fyne.NewSize(480, 220))
	form.Show()
}

func (s *shell) renderRecord(dir, choice string) {
	s.startJob("render "+filepath.Base(dir)+" ("+choice+")", renderArgs(s.dir, dir, choice))
}

// chooseCompare asks which other runs of the same scenario to compare a
// record with, and asks reproduce which stable facts differ.
func (s *shell) chooseCompare(dir string) {
	candidates := compareCandidates(s.dir, dir)
	if len(candidates) == 0 {
		dialog.ShowInformation("Nothing to compare with", "There is no other record of this scenario in the runs directory.", s.window)
		return
	}
	byID := make(map[string]string, len(candidates))
	ids := make([]string, len(candidates))
	for i, r := range candidates {
		ids[i], byID[r.RunID] = r.RunID, r.Dir
	}
	checks := widget.NewCheckGroup(ids, nil)
	checks.SetSelected(ids[:1])
	form := dialog.NewForm("Compare "+filepath.Base(dir), "Compare", "Cancel", []*widget.FormItem{
		{Text: "With", Widget: container.NewVScroll(checks), HintText: "Other records of the same scenario, newest first."},
	}, func(ok bool) {
		if !ok || len(checks.Selected) == 0 {
			return
		}
		others := make([]string, 0, len(checks.Selected))
		for _, id := range ids {
			if slices.Contains(checks.Selected, id) {
				others = append(others, byID[id])
			}
		}
		s.compareRecords(dir, others)
	}, s.window)
	form.Resize(fyne.NewSize(620, 420))
	form.Show()
}

func (s *shell) compareRecords(dir string, others []string) {
	s.startJob("compare "+filepath.Base(dir), compareArgs(s.dir, dir, others))
}
