package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

// A run is asked for with the CLI's own flags and nothing else. What changes
// the measurement is typed each time, as on the command line, and never kept.

func TestARunIsAskedForWithTheCLIsOwnFlags(t *testing.T) {
	t.Parallel()

	got, err := runArgs(runRequest{
		RunsDir: "/srv/runs", ScenarioRoot: "/srv/scenarios", Scenario: "hello",
		Params: map[string]string{"version": "1.2.3", "image": "debian:12-slim", "unset": ""},
		Target: "box-a", Keep: true,
		Extra: "--docker-context=remote\n\n  --env-file=C:\\secrets\\hello.env  \n",
	})
	if err != nil {
		t.Fatalf("runArgs: %v", err)
	}
	want := []string{
		"--runs-dir", "/srv/runs", "--scenario-root", "/srv/scenarios",
		"run", "hello", "--output", "json", "--progress", "json",
		"--param", "image=debian:12-slim", "--param", "version=1.2.3",
		"--target", "box-a", "--keep",
		"--docker-context=remote", "--env-file=C:\\secrets\\hello.env",
	}
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n%q\nwant\n%q", got, want)
	}
}

// Extra flags are passed as typed, one a line, with no shell between: a line
// that is not a flag is refused rather than guessed at.
func TestAnExtraLineThatIsNotAFlagIsRefused(t *testing.T) {
	t.Parallel()

	_, err := runArgs(runRequest{RunsDir: "r", ScenarioRoot: "s", Scenario: "hello", Extra: "--keep\nrm -rf /"})
	if err == nil || !strings.Contains(err.Error(), "rm -rf /") {
		t.Errorf("err = %v", err)
	}
}

func TestDoctorIsAskedAboutTheSameRun(t *testing.T) {
	t.Parallel()

	got, err := doctorArgs(runRequest{
		RunsDir: "/srv/runs", ScenarioRoot: "/srv/scenarios", Scenario: "hello",
		Params: map[string]string{"image": "debian:12-slim"}, Target: "box-a", Keep: true,
		Extra: "--docker-context=remote",
	})
	if err != nil {
		t.Fatalf("doctorArgs: %v", err)
	}
	want := []string{
		"--runs-dir", "/srv/runs", "--scenario-root", "/srv/scenarios",
		"doctor", "hello",
		"--param", "image=debian:12-slim", "--target", "box-a", "--docker-context=remote",
	}
	if !slices.Equal(got, want) {
		t.Errorf("doctorArgs =\n%q\nwant\n%q", got, want)
	}
}

func exampleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTheScenariosScreenListsWhatIsUnderTheScenarioRoot(t *testing.T) {
	test.NewTempApp(t)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", "scenario.yaml"), []byte("{not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "hello"), os.DirFS(filepath.Join(exampleRoot(t), "hello"))); err != nil {
		t.Fatal(err)
	}

	v := newScenariosView(root, "TESTEASE_SCENARIO_ROOT", func(string) {})
	if len(v.all) != 2 {
		t.Fatalf("scenarios = %+v", v.all)
	}
	if got := v.cellText(0, scolName); got != "broken" {
		t.Errorf("first = %q", got)
	}
	if got := v.cellText(0, scolQuestion); !strings.HasPrefix(got, "will not load") {
		t.Errorf("a scenario that will not load says so: %q", got)
	}
	if got := v.cellText(1, scolQuestion); !strings.HasPrefix(got, "Does this machine run a scenario") {
		t.Errorf("question = %q", got)
	}
}

// Opening a scenario offers its params with their defaults, empty: a value
// left empty is the scenario's default, and nothing typed is remembered.
func TestAScenarioOpensOnItsParamsAndNothingIsRemembered(t *testing.T) {
	test.NewTempApp(t)

	var asked []string
	v, err := newScenarioView(scenarioContext{
		runsDir: "/srv/runs", root: exampleRoot(t), name: "hello",
		start: func(title string, args []string) { asked = args },
	}, func() {})
	if err != nil {
		t.Fatalf("newScenarioView: %v", err)
	}
	entry, ok := v.params["image"]
	if !ok {
		t.Fatalf("params = %v", v.params)
	}
	if entry.Text != "" || entry.PlaceHolder != "debian:12-slim" {
		t.Errorf("image entry = %q placeholder %q", entry.Text, entry.PlaceHolder)
	}

	entry.SetText("debian:13-slim")
	v.run()
	if !slices.Contains(asked, "image=debian:13-slim") || !slices.Contains(asked, "run") {
		t.Errorf("asked %q", asked)
	}

	again, err := newScenarioView(scenarioContext{runsDir: "/srv/runs", root: exampleRoot(t), name: "hello", start: func(string, []string) {}}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if again.params["image"].Text != "" {
		t.Errorf("a param typed last time is still there: %q", again.params["image"].Text)
	}
}
