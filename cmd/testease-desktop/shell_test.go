package main

import (
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

// Fyne grows a window to fit whatever its content asks for. Nothing on a
// screen - a record's output, a long label from someone's scenario - may push
// the window past the screen; a screen that asks for more scrolls instead.
func TestAScreenThatAsksForMoreRoomScrollsRatherThanGrowingTheWindow(t *testing.T) {
	test.NewTempApp(t)

	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	w.SetContent(s.content)

	huge := canvas.NewRectangle(nil)
	huge.SetMinSize(fyne.NewSize(5000, 3000))
	s.show(nil, huge)

	if got := s.content.MinSize(); got.Width > openingSize.Width || got.Height > openingSize.Height {
		t.Errorf("the window is asked for %v, want no more than %v", got, openingSize)
	}
}

// The sections are Runs, Batches, Campaigns and Settings. A record opened from
// a batch goes back to that batch, not to the Runs listing.
func TestARecordOpenedFromABatchGoesBackToTheBatch(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	smokeSet(t, runs, true)
	installCheck(t, runs, "install-check-20261123T171533Z-install-old")

	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, runs, "--runs-dir", func(string) {})
	w.SetContent(s.content)

	s.showBatches()
	if s.section != sectionBatches {
		t.Fatalf("section = %v", s.section)
	}
	s.batches.onOpen(s.batches.all[0])
	b, ok := s.current.(*batchView)
	if !ok {
		t.Fatalf("showing %T, want the batch", s.current)
	}
	b.open(0)
	d, ok := s.current.(*detailView)
	if !ok {
		t.Fatalf("showing %T, want the record", s.current)
	}
	if d.backLabel != "Batch" {
		t.Errorf("back says %q", d.backLabel)
	}
	d.back()
	if _, ok := s.current.(*batchView); !ok {
		t.Errorf("back went to %T, want the batch", s.current)
	}
}

// A run started from a scenario shows live under Activity, and the window
// asks before quitting while one is going.
func TestARunStartedHereIsShownUnderActivity(t *testing.T) {
	test.NewTempApp(t)
	t.Setenv(fakeEnv, "finish")

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	s.useTools(tools{testease: self, testeaseFrom: "--testease"})
	s.uiCall = func(func()) {}
	w.SetContent(s.content)

	s.startJob("run hello", []string{"run", "hello"})
	jv, ok := s.current.(*jobView)
	if !ok || len(s.jobs) != 1 || s.section != sectionActivity {
		t.Fatalf("showing %T, jobs %d, section %v", s.current, len(s.jobs), s.section)
	}
	jv.j.wait()
	jv.refresh()
	if text, _ := jv.statusText(); text != "measured" {
		t.Errorf("status = %q", text)
	}
	if got := jv.cellText(1, jcolStatus); got != "measured" {
		t.Errorf("step 2 = %q", got)
	}

	s.showActivity()
	a := s.current.(*activityView)
	if got := a.cellText(0, acolCommand); got != "run hello" {
		t.Errorf("activity = %q", got)
	}
}
