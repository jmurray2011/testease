package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/livebrowser"
	"github.com/jmurray2011/testease/internal/record"
)

// writeEvents writes an events file of the lines given, each a kind, a step
// and the step's label, the way the runner does.
func writeEvents(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, record.StepsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, record.EventsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func started(seq int, label, ts string) string {
	return fmt.Sprintf(`{"schema":1,"seq":%d,"ts":%q,"kind":"step-started","step":%d,"data":{"label":%q,"host":"box"}}`, seq*10, ts, seq, label)
}

func operation(seq int) string {
	return fmt.Sprintf(`{"schema":1,"seq":%d,"ts":"2026-11-27T01:00:00Z","kind":"operation","step":%d,"data":{"rc":0}}`, seq*10+1, seq)
}

func writeStepOutput(t *testing.T, dir, rel, body string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunningStepsIsTheStepWithNoOutcome(t *testing.T) {
	dir := t.TempDir()
	writeEvents(
		t, dir,
		started(1, "PRECONDITION: seed", "2026-11-27T00:00:00Z"),
		operation(1),
		started(2, "UPGRADE: run the installer", "2026-11-27T00:01:00Z"),
		`{"kind":"fact","step":2,"data":{"key":"half`, // being written now
	)
	out, _ := record.StepPaths(2, "UPGRADE: run the installer")
	want := writeStepOutput(t, dir, out, "extracting\n")

	steps := runningSteps(dir)
	if len(steps) != 1 {
		t.Fatalf("running steps = %+v, want only step 2", steps)
	}
	if s := steps[0]; s.Seq != 2 || s.Label != "UPGRADE: run the installer" || s.Host != "box" || s.Out != want {
		t.Errorf("running step = %+v, want step 2 writing %s", s, want)
	}
	if steps[0].Err != "" {
		t.Errorf("stderr = %q, want none before the runner makes it", steps[0].Err)
	}
}

func TestRunningStepsIsNothingOnceTheRunFinished(t *testing.T) {
	dir := t.TempDir()
	writeEvents(
		t, dir,
		started(1, "one", "2026-11-27T00:00:00Z"),
		`{"kind":"run-finished","data":{}}`,
	)
	if steps := runningSteps(dir); len(steps) != 0 {
		t.Errorf("running steps = %+v, want none in a finished run", steps)
	}
}

func TestRunningStepsAcrossCellsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	writeEvents(t, dir, `{"kind":"run-started","data":{}}`)
	writeEvents(t, filepath.Join(dir, record.CellsDir, "7.2"), started(1, "install", "2026-11-27T00:00:00Z"))
	writeEvents(t, filepath.Join(dir, record.CellsDir, "7.3"), started(3, "upgrade", "2026-11-27T00:05:00Z"))

	steps := runningSteps(dir)
	if len(steps) != 2 {
		t.Fatalf("running steps = %+v, want one in each cell", steps)
	}
	if steps[0].name() != "7.3 · 3 · upgrade" || steps[1].name() != "7.2 · 1 · install" {
		t.Errorf("order = %q, %q; want the newest first", steps[0].name(), steps[1].name())
	}
}

func TestStepFileIsTheLatestAttemptAndNotANeighbour(t *testing.T) {
	dir := t.TempDir()
	writeEvents(t, dir)
	out, _ := record.StepPaths(4, "copy")
	first := writeStepOutput(t, dir, out, "attempt one\n")
	second := writeStepOutput(t, dir, strings.TrimSuffix(out, ".out")+".2.out", "attempt two\n")
	// A different step whose slug starts the same way.
	other, _ := record.StepPaths(4, "copy back")
	writeStepOutput(t, dir, other, "not this\n")
	// Make the second attempt the newest, whatever the clock's resolution.
	if info, err := os.Stat(first); err == nil {
		later := info.ModTime().Add(2e9)
		_ = os.Chtimes(second, later, later)
	}

	if got := stepFile(dir, 4, "copy", ".out"); got != second {
		t.Errorf("stepFile = %s, want the second attempt %s", got, second)
	}
}

func TestLiveViewShowsTheRunningStepAndFollowsIt(t *testing.T) {
	test.NewTempApp(t)
	dir := t.TempDir()
	writeEvents(t, dir, started(1, "install", "2026-11-27T00:00:00Z"))
	out, _ := record.StepPaths(1, "install")
	path := writeStepOutput(t, dir, out, "unpacking\n")

	v := newLiveView()
	v.onUI = func(f func()) { f() }
	v.dir = dir
	v.update()
	if !strings.Contains(v.heading.Text, "Now: 1 · install · on box") {
		t.Errorf("heading = %q, want the running step", v.heading.Text)
	}
	if got := v.stdout.Text(); got != "unpacking" && got != "unpacking\n" {
		t.Errorf("stdout = %q, want the step's output", got)
	}

	if err := os.WriteFile(path, []byte("unpacking\nconfiguring\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v.update()
	if !strings.Contains(v.stdout.Text(), "configuring") {
		t.Errorf("stdout = %q, want the line written since", v.stdout.Text())
	}

	// The step ends: its last lines stay on screen, marked finished.
	writeEvents(t, dir, started(1, "install", "2026-11-27T00:00:00Z"), operation(1))
	v.update()
	if !strings.HasPrefix(v.heading.Text, "Last: 1 · install") {
		t.Errorf("heading = %q, want the finished step kept", v.heading.Text)
	}
	if !strings.Contains(v.stdout.Text(), "configuring") {
		t.Errorf("stdout = %q, want the finished step's output kept", v.stdout.Text())
	}
}

func TestLiveViewShowsTheNewestBrowserFrame(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	dir := filepath.Join(runs, "smoke-20261127T000000Z")
	writeEvents(t, dir, started(2, "BROWSER: sign in", "2026-11-27T00:00:00Z"))

	ws, err := os.MkdirTemp(runs, livebrowser.WorkspacePattern[:len(livebrowser.WorkspacePattern)-1])
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(ws, livebrowser.Dir)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(dir)
	owner := fmt.Sprintf(`{"record":%q,"step":2}`, abs)
	if err := os.WriteFile(filepath.Join(live, livebrowser.OwnerFile), []byte(owner), 0o644); err != nil {
		t.Fatal(err)
	}

	v := newLiveView()
	v.dir = dir
	v.update()
	if v.frame.Resource != nil || v.frameNote.Text != noFrames {
		t.Errorf("before a frame: resource %v, note %q; want none and the explanation", v.frame.Resource, v.frameNote.Text)
	}

	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "frame-7.jpg"), jpg.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "frame-7.json"), []byte(`{"url":"https://app.example/login"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v.update()
	if v.frame.Resource == nil {
		t.Fatal("the frame is not shown")
	}
	if !strings.Contains(v.frameNote.Text, "https://app.example/login") {
		t.Errorf("note = %q, want the page's address", v.frameNote.Text)
	}

	// The step ends and its workspace goes: the last frame stays, said to be
	// the last.
	shown := v.frame.Resource
	_ = os.RemoveAll(ws)
	writeEvents(t, dir, started(2, "BROWSER: sign in", "2026-11-27T00:00:00Z"), operation(2))
	v.update()
	if v.frame.Resource != shown || !strings.Contains(v.frameNote.Text, "last frame") {
		t.Errorf("after the step: resource kept %v, note %q", v.frame.Resource == shown, v.frameNote.Text)
	}
}

func TestJobsAreStartedWithLiveBrowserUnlessSetAlready(t *testing.T) {
	if got := jobEnv([]string{"PATH=/bin"}); got[len(got)-1] != "TESTEASE_LIVE_BROWSER=1" {
		t.Errorf("jobEnv = %v, want live browser on", got)
	}
	if got := jobEnv([]string{"TESTEASE_LIVE_BROWSER=0"}); len(got) != 1 {
		t.Errorf("jobEnv = %v, want the person's own setting kept", got)
	}
}
