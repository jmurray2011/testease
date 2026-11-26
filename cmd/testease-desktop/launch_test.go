package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// A run is launched from a form: an axis is picked from the values the
// scenario declares, a named preset fills the form on purpose, and any record
// can be run again with what it was run with.

// sweepRoot is a scenario root holding hello, made into a sweep: an axis with
// declared values, and a param whose name says it is a secret.
func sweepRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(exampleRoot(t), "hello")
	dst := filepath.Join(root, "sweep")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), "name: hello", "name: sweep", 1)
	text = strings.Replace(text, `  image: {default: "debian:12-slim"}`,
		`  image: {default: "debian:12-slim"}
  version: {axis: true, values: "7.2.6, 7.3.0, 7.4.1"}
  api_token: {default: ""}`, 1)
	if err := os.WriteFile(filepath.Join(dst, "scenario.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func openSweep(t *testing.T, root string, asked *[]string) *scenarioView {
	t.Helper()
	v, err := newScenarioView(scenarioContext{
		runsDir: "/srv/runs", root: root, name: "sweep",
		start: func(_ string, args []string) {
			if asked != nil {
				*asked = args
			}
		},
	}, func() {})
	if err != nil {
		t.Fatalf("newScenarioView: %v", err)
	}
	return v
}

func TestAnAxisIsPickedFromTheValuesTheScenarioDeclares(t *testing.T) {
	test.NewTempApp(t)
	var asked []string
	v := openSweep(t, sweepRoot(t), &asked)
	pick, ok := v.picks["version"]
	if !ok {
		t.Fatalf("no picker for the axis: %v", v.picks)
	}
	if !slices.Equal(pick.checks.Options, []string{"7.2.6", "7.3.0", "7.4.1"}) || len(pick.checks.Selected) != 3 {
		t.Fatalf("options %q selected %q", pick.checks.Options, pick.checks.Selected)
	}
	// Every declared value is the scenario's own sweep: nothing is passed.
	v.run()
	if slices.Contains(asked, "--matrix") {
		t.Errorf("the whole sweep was passed as a flag: %q", asked)
	}
	pick.checks.SetSelected([]string{"7.2.6", "7.4.1"})
	pick.other.SetText("7.5.0")
	v.run()
	if !slices.Contains(asked, "version=7.2.6,7.4.1,7.5.0") {
		t.Errorf("asked %q", asked)
	}
	pick.checks.SetSelected(nil)
	pick.other.SetText("")
	asked = nil
	v.run()
	if asked != nil || v.problem.Hidden {
		t.Errorf("a sweep over nothing was started: %q", asked)
	}
}

func TestAPresetFillsTheFormOnlyWhenPicked(t *testing.T) {
	test.NewTempApp(t)
	root := sweepRoot(t)
	v := openSweep(t, root, nil)
	v.params["image"].SetText("debian:13-slim")
	v.params["api_token"].SetText("hunter2")
	v.picks["version"].checks.SetSelected([]string{"7.3.0"})
	v.extra.SetText("--docker-context=remote")
	v.savePreset("nightly on 7.3")

	again := openSweep(t, root, nil)
	if again.params["image"].Text != "" {
		t.Errorf("a new form is not empty: %q", again.params["image"].Text)
	}
	if !slices.Contains(again.preset.Options, "nightly on 7.3") {
		t.Fatalf("presets %q", again.preset.Options)
	}
	again.preset.SetSelected("nightly on 7.3")
	if again.params["image"].Text != "debian:13-slim" || again.extra.Text != "--docker-context=remote" {
		t.Errorf("filled image %q extra %q", again.params["image"].Text, again.extra.Text)
	}
	if got := again.picks["version"].checks.Selected; !slices.Equal(got, []string{"7.3.0"}) {
		t.Errorf("axis %q", got)
	}
	// A secret is never kept in a preset.
	if again.params["api_token"].Text != "" {
		t.Errorf("a secret came back from a preset")
	}
	again.deletePreset("nightly on 7.3")
	if slices.Contains(openSweep(t, root, nil).preset.Options, "nightly on 7.3") {
		t.Error("a deleted preset is still offered")
	}
}

func TestARecordIsRunAgainWithWhatItWasRunWith(t *testing.T) {
	d := browse.Detail{
		Run:    browse.Run{RunID: "sweep-20261124T010000Z", Scenario: "sweep"},
		Params: map[string]string{"image": "debian:13-slim", "api_token": "hunter2"},
	}
	m := &record.Matrix{Axes: map[string][]string{"version": {"7.2.6", "7.4.1"}}}
	r, dropped := againRequest(d, m)
	if r.Params["image"] != "debian:13-slim" || r.Matrix["version"] != "7.2.6,7.4.1" {
		t.Errorf("request %+v", r)
	}
	if _, ok := r.Params["api_token"]; ok || !slices.Equal(dropped, []string{"api_token"}) {
		t.Errorf("secret copied: %+v dropped %q", r.Params, dropped)
	}
}

func TestRunAgainOpensTheScenarioFilledIn(t *testing.T) {
	test.NewTempApp(t)
	root := sweepRoot(t)
	runs := t.TempDir()
	dir := recorded(t, runs, "sweep-20261124T010000Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID: "sweep-20261124T010000Z", Scenario: record.ScenarioRef{Name: "sweep"},
			Params: map[string]string{"image": "debian:13-slim"},
			Matrix: &record.Matrix{Axes: map[string][]string{"version": {"7.3.0"}}},
		})
	})
	s := shellWithFake(t, runs)
	s.tools.scenarioRoot = root
	s.openRecord(dir, "Runs", s.showRuns)
	d := s.current.(*detailView)
	if d.again.Hidden {
		t.Fatal("no Run again on a record")
	}
	test.Tap(d.again.Button)
	v, ok := s.current.(*scenarioView)
	if !ok {
		t.Fatalf("opened %T", s.current)
	}
	if v.params["image"].Text != "debian:13-slim" || !slices.Equal(v.picks["version"].checks.Selected, []string{"7.3.0"}) {
		t.Errorf("image %q version %q", v.params["image"].Text, v.picks["version"].checks.Selected)
	}
	if v.filled.Hidden || !strings.Contains(v.filled.Text, "sweep-20261124T010000Z") {
		t.Errorf("filled note %q", v.filled.Text)
	}
}
