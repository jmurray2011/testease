package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

// Targets are what runs left standing: a failed or interrupted run keeps its
// box for inspection. The app asks testease what is standing and, to clean up,
// asks testease reap, which removes only what it can prove nobody is using.
// It never removes a target by itself.

const standingJSON = `{
  "targets": [
    {"name": "testease-sleepy-20261124T052621Z-box", "where": "context default", "run": "sleepy-20261124T052621Z",
     "host": "box", "backend": "docker", "created": "2026-11-24T05:26:21Z", "fate": "orphaned",
     "shell": "docker exec -it testease-sleepy-20261124T052621Z-box bash"},
    {"name": "testease-hello-20261124T060000Z-box", "where": "context default", "run": "hello-20261124T060000Z",
     "host": "box", "backend": "docker", "created": "2026-11-24T06:00:00Z", "fate": "live",
     "shell": "docker exec -it testease-hello-20261124T060000Z-box bash"}
  ],
  "notices": ["skipped context remote: daemon not answering"]
}`

const reapPreviewJSON = `{
  "targets": [
    {"kind": "container", "fate": "orphaned", "where": "context default", "name": "testease-sleepy-20261124T052621Z-box",
     "run": "sleepy-20261124T052621Z", "reason": "its run (pid 4242) is gone"},
    {"kind": "network", "fate": "orphaned", "where": "context default", "name": "testease-sleepy-20261124T052621Z",
     "run": "sleepy-20261124T052621Z", "reason": "its run (pid 4242) is gone"},
    {"kind": "container", "fate": "live", "where": "context default", "name": "testease-hello-20261124T060000Z-box",
     "run": "hello-20261124T060000Z", "reason": "its run (pid 777) is still going"}
  ],
  "removable": 2, "removed": [], "notices": []
}`

const reapForceJSON = `{
  "targets": [], "removable": 2,
  "removed": ["testease-sleepy-20261124T052621Z-box", "testease-sleepy-20261124T052621Z"],
  "notices": []
}`

// fakeCLI answers testease commands from canned output and remembers what it
// was asked.
type fakeCLI struct {
	asked   [][]string
	answers map[string]string
}

func (f *fakeCLI) call(_ context.Context, args []string) ([]byte, error) {
	f.asked = append(f.asked, args)
	key := strings.Join(args, " ")
	// The longest matching key wins, so "reap --force" beats "reap" whatever
	// order the map gives.
	best, found := "", false
	for k := range f.answers {
		if strings.Contains(key, k) && len(k) >= len(best) {
			best, found = k, true
		}
	}
	if found {
		return []byte(f.answers[best]), nil
	}
	return []byte(`{"targets": [], "notices": []}`), nil
}

func newFakeTargets(t *testing.T, cli *fakeCLI) *targetsView {
	t.Helper()
	v := newTargetsView("/srv/runs", cli.call, func(string) {})
	v.async = func(f func()) { f() }
	v.onUI = func(f func()) { f() }
	v.reload()
	return v
}

func TestTheTargetsScreenListsWhatTesteaseSaysIsStanding(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)

	if want := []string{"--runs-dir", "/srv/runs", "targets", "--output", "json"}; !slices.Equal(cli.asked[0], want) {
		t.Errorf("asked %q, want %q", cli.asked[0], want)
	}
	want := map[int]string{
		tcolTarget: "testease-sleepy-20261124T052621Z-box",
		tcolHost:   "box",
		tcolRunID:  "sleepy-20261124T052621Z",
		tcolFate:   "kept, run gone",
	}
	for col, text := range want {
		if got := v.cellText(0, col); got != text {
			t.Errorf("column %d = %q, want %q", col, got, text)
		}
	}
	if got := v.cellText(1, tcolFate); got != "run still going" {
		t.Errorf("live fate = %q", got)
	}
	if !v.message.Visible() || !strings.Contains(v.message.Text, "daemon not answering") {
		t.Errorf("a daemon that did not answer is not shown as empty: %q", v.message.Text)
	}
}

func TestPickingATargetShowsHowToGetOntoIt(t *testing.T) {
	test.NewTempApp(t)

	v := newFakeTargets(t, &fakeCLI{answers: map[string]string{"targets": standingJSON}})
	v.pick(0)
	if got := v.shell.Text; got != "docker exec -it testease-sleepy-20261124T052621Z-box bash" {
		t.Errorf("shell = %q", got)
	}
}

// Removing is reap's call: the app shows what reap would remove and why, asks,
// and only then runs reap --force.
func TestRemovingKeptTargetsShowsWhatReapWouldRemoveThenAsks(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{
		"reap --force": reapForceJSON,
		"reap":         reapPreviewJSON,
		"targets":      standingJSON,
	}}
	v := newFakeTargets(t, cli)
	var question string
	v.ask = func(_, msg string, yes func()) { question = msg; yes() }

	v.removeKept()

	if !strings.Contains(question, "testease-sleepy-20261124T052621Z-box") || !strings.Contains(question, "its run (pid 4242) is gone") {
		t.Errorf("question = %q, want each target and why reap may remove it", question)
	}
	if strings.Contains(question, "testease-hello") {
		t.Errorf("question = %q offers a target whose run is still going", question)
	}
	var reaps [][]string
	for _, a := range cli.asked {
		if slices.Contains(a, "reap") {
			reaps = append(reaps, a)
		}
	}
	if len(reaps) != 2 || slices.Contains(reaps[0], "--force") || !slices.Contains(reaps[1], "--force") {
		t.Fatalf("reap asked %q, want a look then --force", reaps)
	}
	if !strings.Contains(v.message.Text, "Removed 2") {
		t.Errorf("message = %q", v.message.Text)
	}
}

func TestNothingRemovableIsSaidWithoutAsking(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"reap": `{"targets": [], "removable": 0, "removed": [], "notices": []}`}}
	v := newFakeTargets(t, cli)
	v.ask = func(string, string, func()) { t.Error("asked to remove nothing") }

	v.removeKept()
	for _, a := range cli.asked {
		if slices.Contains(a, "--force") {
			t.Errorf("ran %q", a)
		}
	}
	if !strings.Contains(v.message.Text, "Nothing") {
		t.Errorf("message = %q", v.message.Text)
	}
}

// Saying no puts the screen back as it was, not stuck on the question.
func TestSayingNoToRemovingLeavesNoQuestionShowing(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"reap": reapPreviewJSON, "targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.ask = func(string, string, func()) {}

	v.removeKept()
	if strings.Contains(v.message.Text, "Asking") {
		t.Errorf("message = %q after no", v.message.Text)
	}
}

// Once the list is asked again, a target picked before may be gone, so the
// pick is let go with it.
func TestReloadingLetsGoOfThePickedTarget(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.pick(0)
	cli.answers["targets"] = `{"targets": [], "notices": []}`
	v.reload()

	if strings.Contains(v.shell.Text, "docker exec") {
		t.Errorf("shell = %q after the target went", v.shell.Text)
	}
	if !v.copy.Disabled() || !v.openRec.Disabled() {
		t.Error("Copy or Open run still enabled with nothing picked")
	}
}
