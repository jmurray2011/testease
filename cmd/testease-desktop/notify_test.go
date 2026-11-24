package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// A job that ends while the app is in the background says so with a desktop
// notification; one that ends in front of the person needs none.

func TestAFinishedJobIsNamedWithHowItEnded(t *testing.T) {
	cases := []struct {
		isRun bool
		s     jobState
		want  string
	}{
		{true, jobState{Done: true, Status: "measured"}, "Finished: measured"},
		{true, jobState{Done: true, Status: "probe-fail", Exit: 1}, "Finished: probe-fail"},
		{false, jobState{Done: true}, "Finished: exit 0"},
		{false, jobState{Done: true, Exit: 12}, "Finished: exit 12"},
	}
	for _, c := range cases {
		v := &jobView{isRun: c.isRun, s: c.s}
		if got := doneText(v); got != c.want {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
	}
}

func TestOnlyAJobEndingInTheBackgroundNotifies(t *testing.T) {
	test.NewTempApp(t)
	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	var sent []string
	s.notify = func(title, body string) { sent = append(sent, title+": "+body) }

	v := &jobView{title: "run hello", s: jobState{Done: true}}
	s.finished(v)
	if len(sent) != 0 {
		t.Errorf("notified in the foreground: %v", sent)
	}
	s.background = true
	s.finished(v)
	if len(sent) != 1 || sent[0] != "run hello: Finished: exit 0" {
		t.Errorf("sent %v", sent)
	}
}
