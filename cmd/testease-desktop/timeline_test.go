package main

import (
	"encoding/json"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/record"
)

// A run's timeline is when each step started, read off the record's own
// event times, and how long it took by its own measure.

func event(t *testing.T, at time.Duration, kind record.Kind, step int, data any) record.Event {
	t.Helper()
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	return record.Event{TS: base.Add(at), Kind: kind, Step: step, Data: body}
}

func TestStepsArePlacedWhereTheyStartedAndSizedByWhatTheyMeasured(t *testing.T) {
	s := time.Second
	events := []record.Event{
		event(t, 0, record.KindRunStarted, 0, record.RunStarted{RunID: "r"}),
		event(t, 2*s, record.KindStepStarted, 1, record.StepStarted{Label: "install"}),
		event(t, 12*s, record.KindOperation, 1, record.Operation{DurationS: 9.5, Status: record.StatusMeasured}),
		event(t, 13*s, record.KindStepStarted, 2, record.StepStarted{Label: "check"}),
		event(t, 14*s, record.KindOperation, 2, record.Operation{DurationS: 1, Status: record.StatusProbeFail}),
		event(t, 15*s, record.KindStepStarted, 3, record.StepStarted{Label: "hangs"}),
		event(t, 40*s, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusInterrupted}),
	}
	spans, total := timeline(events)
	if total != 40 {
		t.Errorf("total %v, want 40", total)
	}
	want := []span{
		{Seq: 1, Label: "install", Status: "measured", Start: 2, Dur: 9.5},
		{Seq: 2, Label: "check", Status: "probe-fail", Start: 13, Dur: 1},
		// No outcome: it ran until the record stops saying anything.
		{Seq: 3, Label: "hangs", Start: 15, Dur: 25},
	}
	if len(spans) != len(want) {
		t.Fatalf("spans %+v", spans)
	}
	for i := range want {
		if spans[i] != want[i] {
			t.Errorf("span %d = %+v, want %+v", i, spans[i], want[i])
		}
	}
}

// Only the attempt that stands is drawn, as everywhere else.
func TestTheTimelineIsOfTheLatestAttempt(t *testing.T) {
	s := time.Second
	events := []record.Event{
		event(t, 0, record.KindRunStarted, 0, record.RunStarted{RunID: "r"}),
		event(t, 1*s, record.KindStepStarted, 1, record.StepStarted{Label: "old"}),
		event(t, 100*s, record.KindRunStarted, 0, record.RunStarted{RunID: "r"}),
		event(t, 101*s, record.KindStepStarted, 1, record.StepStarted{Label: "new"}),
		event(t, 103*s, record.KindOperation, 1, record.Operation{DurationS: 2, Status: record.StatusMeasured}),
	}
	spans, total := timeline(events)
	if len(spans) != 1 || spans[0].Label != "new" || spans[0].Start != 1 || total != 3 {
		t.Errorf("spans %+v total %v", spans, total)
	}
}

func TestTheSlowestStepsAreCalledOut(t *testing.T) {
	spans := []span{{Seq: 1, Dur: 1}, {Seq: 2, Dur: 30}, {Seq: 3, Dur: 2}, {Seq: 4, Dur: 12}, {Seq: 5, Dur: 0.5}}
	got := slowest(spans, 2)
	if len(got) != 2 || !got[2] || !got[4] {
		t.Errorf("slowest %v, want steps 2 and 4", got)
	}
	if len(slowest(spans[:3], 3)) != 0 {
		t.Error("with three steps or fewer, nothing is called slow")
	}
}

func TestARecordShowsItsTimelineAndATappedStepOpens(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	dir := installCheck(t, runs, "install-check-20261123T171533Z")
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if v.timeline == nil || len(v.timeline.spans) != 2 {
		t.Fatalf("timeline %+v", v.timeline)
	}
	v.tabs.SelectIndex(3)
	v.openStep(1)
	if v.selected != 0 || v.tabs.SelectedIndex() != 0 {
		t.Errorf("selected step %d, tab %d", v.selected, v.tabs.SelectedIndex())
	}
	if v.timeline.selected != 1 {
		t.Errorf("timeline marks step %d", v.timeline.selected)
	}
}
