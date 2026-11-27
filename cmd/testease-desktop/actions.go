package main

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
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
	s.openScenarioFrom(name, "Scenarios", s.showScenarios)
}

func (s *shell) openScenarioFrom(name, from string, back func()) *scenarioView {
	v, err := newScenarioView(scenarioContext{
		runsDir: s.dir, root: s.tools.scenarioRoot, name: name, window: s.window, backLabel: from,
		targets: catalogNames(s.tools.catalog), start: func(title string, args []string) { s.startJob(title, args) },
	}, back)
	if err != nil {
		dialog.ShowError(err, s.window)
		return nil
	}
	s.highlight(sectionScenarios)
	s.show(v, v.content)
	return v
}

// runAgain opens a record's scenario with the form filled from the record,
// for a person to check and run. Nothing starts until they press Run.
func (s *shell) runAgain(d *detailView) {
	name := d.d.Scenario
	if name == "" || s.tools.scenarioRoot == "" {
		dialog.ShowInformation("Run again", "Choose the scenario root on the Scenarios screen first: the record names the scenario, and the scenario's files are needed to run it.", s.window)
		return
	}
	if _, err := os.Stat(scenarioDir(s.tools.scenarioRoot, name)); err != nil || !filepath.IsLocal(name) {
		dialog.ShowInformation("Run again", fmt.Sprintf("There is no scenario %q under %s.", name, s.tools.scenarioRoot), s.window)
		return
	}
	v := s.openScenarioFrom(name, "Run", func() { s.show(d, d.content) })
	if v == nil {
		return
	}
	r, dropped := againRequest(d.d, d.matrix)
	v.fill(r)
	text := "Filled from " + d.d.RunID + ". Check it, then Run."
	if len(dropped) > 0 {
		text += " Secrets are not copied from a record; type " + strings.Join(dropped, ", ") + " again."
	}
	v.say(v.filled, text)
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
	j, err := startJob(s.tools.testease, args, jobEnv(os.Environ()), func() { s.onUI(v.refresh) })
	if err != nil {
		v.fail(err)
		return v
	}
	v.attach(j)
	s.countJobs()
	go func() {
		j.wait()
		s.onUI(func() {
			v.refresh()
			s.finished(v)
		})
	}()
	return v
}

// finished tells a person who has gone elsewhere that a job ended.
func (s *shell) finished(v *jobView) {
	s.countJobs()
	if s.background && s.notify != nil {
		s.notify(v.title, doneText(v))
	}
}

// doneText is how a job ended, as a notification says it.
func doneText(v *jobView) string {
	text, _ := v.statusText()
	return "Finished: " + text
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
	s.startRecordJob(dir, "render "+filepath.Base(dir)+" ("+choice+")", renderArgs(s.dir, dir, choice))
}

// chooseCompare asks which other runs of the same scenario to put beside a
// record, and back returns to the record.
func (s *shell) chooseCompare(dir string, back func()) {
	candidates := compareCandidates(s.dir, dir)
	if len(candidates) == 0 {
		dialog.ShowInformation("Nothing to compare with", "There is no other record of this scenario in the runs directory.", s.window)
		return
	}
	s.pickRuns("Compare "+filepath.Base(dir), "Other records of the same scenario, newest first.", candidates, 1, func(dirs []string) {
		s.compareRecords(append([]string{dir}, dirs...), "Run", back)
	})
}

// chooseRunsToCompare asks which of the runs the Runs screen lists to put
// side by side.
func (s *shell) chooseRunsToCompare() {
	candidates := append([]browse.Run(nil), s.runs.shown...)
	if len(candidates) < 2 {
		dialog.ShowInformation("Nothing to compare", "Comparing takes two runs or more, and the Runs screen lists fewer. Clear its filters to list more.", s.window)
		return
	}
	s.pickRuns("Compare runs", "The runs listed, newest first. Pick two or more; any scenario.", candidates, 2, func(dirs []string) {
		s.compareRecords(dirs, "Runs", s.showRuns)
	})
}

// pickRuns asks which runs to take, at least the given number, in a dialog of
// checkboxes.
func (s *shell) pickRuns(title, hint string, candidates []browse.Run, least int, done func(dirs []string)) {
	byID := make(map[string]string, len(candidates))
	ids := make([]string, len(candidates))
	for i, r := range candidates {
		ids[i], byID[r.RunID] = r.RunID, r.Dir
	}
	checks := widget.NewCheckGroup(ids, nil)
	if least == 1 {
		checks.SetSelected(ids[:1])
	}
	form := dialog.NewForm(title, "Compare", "Cancel", []*widget.FormItem{
		{Text: "Runs", Widget: pickerList(checks), HintText: hint},
	}, func(ok bool) {
		if !ok {
			return
		}
		if len(checks.Selected) < least {
			dialog.ShowInformation("Pick more runs", fmt.Sprintf("Pick at least %d runs to compare.", least), s.window)
			return
		}
		dirs := make([]string, 0, len(checks.Selected))
		for _, id := range ids {
			if slices.Contains(checks.Selected, id) {
				dirs = append(dirs, byID[id])
			}
		}
		done(dirs)
	}, s.window)
	form.Resize(fyne.NewSize(620, 420))
	form.Show()
}

// pickerList holds a list of runs to pick from at a height that shows a dozen
// of them, not the one row a scroll asks for on its own.
func pickerList(checks *widget.CheckGroup) fyne.CanvasObject {
	room := canvas.NewRectangle(color.Transparent)
	room.SetMinSize(fyne.NewSize(520, 320))
	return container.NewStack(room, container.NewVScroll(checks))
}

// compareRecords puts runs side by side. Runs of one scenario can also be
// put to the reproduce judge from there.
func (s *shell) compareRecords(dirs []string, from string, back func()) {
	v, err := newCompareView(dirs, from, back)
	if err != nil {
		dialog.ShowError(err, s.window)
		return
	}
	v.window = s.window
	s.highlightFrom(from)
	if v.oneScenario() {
		v.reproduce.set(func() {
			d := v.dirs()
			s.startJob("reproduce "+filepath.Base(d[0]), compareArgs(s.dir, d[0], d[1:]))
		})
	}
	s.show(v, v.content)
}

// startRecordJob starts a job about a record already on disk, which Open
// record opens whatever the command printed.
func (s *shell) startRecordJob(dir, title string, args []string) {
	if jv := s.startJob(title, args); jv != nil {
		jv.record = dir
	}
}

// jobEnv is the environment a job starts with. Runs this app starts can be
// watched, so their drive steps save browser frames for the live pane, unless
// the person set TESTEASE_LIVE_BROWSER themselves. It is an environment value
// rather than a flag so a batch or campaign passes it to every run it starts.
func jobEnv(env []string) []string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "TESTEASE_LIVE_BROWSER=") {
			return env
		}
	}
	return append(env, "TESTEASE_LIVE_BROWSER=1")
}
