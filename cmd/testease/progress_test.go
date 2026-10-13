package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
)

func TestProgressShowsAStartedAndMeasuredStep(t *testing.T) {
	var out bytes.Buffer
	p := newProgressReporter(&out, "plain", 0)
	defer p.Close()

	p.Observe(runner.ProgressEvent{
		RunID: "run-1", Kind: record.KindStepStarted, Step: 3, Total: 14,
		Data: record.StepStarted{Label: "prepare actors", Host: "app"},
	})
	p.Observe(runner.ProgressEvent{
		RunID: "run-1", Kind: record.KindOperation, Step: 3, Total: 14,
		Data: record.Operation{RC: 0, DurationS: 12.3, Status: record.StatusMeasured},
	})

	got := out.String()
	for _, want := range []string{"[3/14] starting: prepare actors", "[3/14] measured rc=0 in 12.3s"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress = %q, want %q", got, want)
		}
	}
}

func TestProgressHeartbeatNamesTheStillRunningStep(t *testing.T) {
	var out bytes.Buffer
	p := newProgressReporter(&out, "plain", 5*time.Millisecond)
	p.Observe(runner.ProgressEvent{
		RunID: "run-1", Kind: record.KindStepStarted, Step: 4, Total: 14,
		Data: record.StepStarted{Label: "wait for restart", Host: "app"},
	})
	time.Sleep(20 * time.Millisecond)
	p.Close()

	if got := out.String(); !strings.Contains(got, "still running") || !strings.Contains(got, "wait for restart") {
		t.Errorf("progress = %q, want a heartbeat naming the active step", got)
	}
}

func TestProgressShowsAndKeepsAliveADeclaredMediaTransfer(t *testing.T) {
	var out bytes.Buffer
	p := newProgressReporter(&out, "plain", 5*time.Millisecond)
	p.Observe(runner.ProgressEvent{
		RunID: "run-1", Kind: record.KindMediaStarted,
		Data: record.MediaStageStarted{
			Host: "hot", Item: 1, Total: 5, Name: "installer", Size: 994_634_752,
		},
	})
	time.Sleep(20 * time.Millisecond)
	p.Observe(runner.ProgressEvent{
		RunID: "run-1", Kind: record.KindMediaStaged,
		Data: record.MediaStaged{
			Host: "hot", Item: 1, Total: 5, Name: "installer", Size: 994_634_752, DurationS: 334.5,
		},
	})
	p.Close()

	got := out.String()
	for _, want := range []string{
		"staging media 1/5 on hot: installer (948.6 MiB)",
		"still staging media 1/5",
		"staged media 1/5 on hot in 334.5s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress = %q, want %q", got, want)
		}
	}
}

func TestJSONProgressIsOneStructuredObjectPerLine(t *testing.T) {
	var out bytes.Buffer
	p := newProgressReporter(&out, "json", 0)
	defer p.Close()
	p.Observe(runner.ProgressEvent{
		RunID: "run-1", Cell: "7.2.5", Kind: record.KindStepStarted, Step: 1, Total: 2,
		Data: record.StepStarted{Label: "inspect", Host: "app"},
	})

	var got progressLine
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("progress is not JSON: %v: %q", err, out.String())
	}
	if got.Event != "step-started" || got.RunID != "run-1" || got.Cell != "7.2.5" || got.Label != "inspect" {
		t.Errorf("progress = %+v", got)
	}
}
