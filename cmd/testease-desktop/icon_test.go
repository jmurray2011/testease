package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

func TestTheAppHasItsOwnIcon(t *testing.T) {
	if appIcon == nil || len(appIcon.Content()) == 0 || appIcon.Name() != "testease.svg" {
		t.Fatalf("icon %v", appIcon)
	}
}

// Every section has an icon, and Activity counts the jobs still going.
func TestTheSectionsShowIconsAndActivityCountsRunningJobs(t *testing.T) {
	test.NewTempApp(t)
	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	for i, b := range s.buttons {
		if b.Icon == nil {
			t.Errorf("%s has no icon", sectionNames[i])
		}
	}
	s.jobs = []*jobView{{j: &job{}}, {j: &job{done: true}}}
	s.tick()
	if got := s.buttons[sectionActivity].Text; got != "Activity  1" {
		t.Errorf("activity reads %q", got)
	}
	s.jobs = nil
	s.tick()
	if got := s.buttons[sectionActivity].Text; got != "Activity" {
		t.Errorf("activity reads %q", got)
	}
}
