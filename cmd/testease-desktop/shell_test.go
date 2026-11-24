package main

import (
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
