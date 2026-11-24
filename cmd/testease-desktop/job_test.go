package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The app starts runs the way campaigns and batches already do: by calling
// the testease binary and reading what it reports. --progress json is the live
// view; --output json says which record the run wrote. Stopping is the same
// interrupt a person sends with Ctrl+C, so the run closes its record saying it
// was cut short.

// fakeEnv makes this test binary act as testease for a test that starts it.
const fakeEnv = "TESTEASE_DESKTOP_FAKE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(fakeTestease(mode))
	}
	os.Exit(m.Run())
}

// fakeTestease reports a two-step run on stderr as testease does. "finish"
// measures both steps; "wait" starts step 2 and holds until interrupted.
func fakeTestease(mode string) int {
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)

	progress := json.NewEncoder(os.Stderr)
	id := "hello-20261124T060000Z"
	say := func(v map[string]any) { v["run_id"] = id; _ = progress.Encode(v) }
	say(map[string]any{"event": "run-started"})
	_, _ = fmt.Fprintln(os.Stderr, "level=INFO msg=\"resolved targets\" hosts=1")
	say(map[string]any{"event": "step-started", "step": 1, "total": 2, "label": "pull image"})
	// As testease does, the label rides step-started alone.
	say(map[string]any{"event": "step-measured", "step": 1, "total": 2, "status": "measured", "rc": 0, "seconds": 1.5})
	say(map[string]any{"event": "step-started", "step": 2, "total": 2, "label": "ask the box"})

	status, code := "measured", 0
	if mode == "wait" {
		select {
		case <-interrupted:
			status, code = "interrupted", 12
		case <-time.After(30 * time.Second):
			return 99
		}
	} else {
		say(map[string]any{"event": "step-measured", "step": 2, "total": 2, "status": "measured", "rc": 0, "seconds": 0.4})
	}
	say(map[string]any{"event": "run-finished", "status": status, "seconds": 2})
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"run_id": id, "status": status})
	return code
}

func fakeJob(t *testing.T, mode string) *job {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	j, err := startJob(self, []string{"run", "hello"}, append(os.Environ(), fakeEnv+"="+mode), func() {})
	if err != nil {
		t.Fatalf("startJob: %v", err)
	}
	return j
}

func TestARunReportsItsStepsAsTheyHappenAndWhichRecordItWrote(t *testing.T) {
	j := fakeJob(t, "finish")
	j.wait()

	s := j.state()
	if !s.Done || s.Exit != 0 || s.RunID != "hello-20261124T060000Z" || s.Status != "measured" {
		t.Fatalf("state = %+v", s)
	}
	if len(s.Steps) != 2 {
		t.Fatalf("steps = %+v", s.Steps)
	}
	if got := s.Steps[0]; got.Label != "pull image" || got.Status != "measured" || got.Running {
		t.Errorf("step 1 = %+v", got)
	}
	if !strings.Contains(s.Log, "resolved targets") {
		t.Errorf("log = %q, want testease's own lines kept", s.Log)
	}
	if strings.Contains(s.Log, `"event"`) {
		t.Errorf("log = %q, want the progress lines read, not echoed", s.Log)
	}
	// The progress is told in words in the output too, so it is never blank
	// while a run is going.
	for _, want := range []string{"step 1 started: pull image", "step 1 measured, rc 0, 1.5 s", "run finished: measured"} {
		if !strings.Contains(s.Log, want) {
			t.Errorf("log = %q, want %q", s.Log, want)
		}
	}
}

func TestStoppingARunInterruptsItSoItClosesItsRecord(t *testing.T) {
	j := fakeJob(t, "wait")

	deadline := time.Now().Add(20 * time.Second)
	for {
		s := j.state()
		if len(s.Steps) == 2 && s.Steps[1].Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("step 2 never started: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := j.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	j.wait()

	s := j.state()
	if s.Exit != 12 || s.Status != "interrupted" || !s.Stopping {
		t.Errorf("state = %+v, want the run interrupted and exit 12", s)
	}
}

// The binary is the flag, then the testease beside this app, then the one on
// PATH; the app says which it found, because two installs differ.
func TestTheTesteaseBinaryIsTheFlagThenBesideThisAppThenOnPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	name := "testease"
	if filepath.Ext(os.Args[0]) == ".exe" || os.PathSeparator == '\\' {
		name += ".exe"
	}
	self := filepath.Join(dir, "testease-desktop")
	onPath := func(string) (string, error) { return "/usr/local/bin/testease", nil }
	nowhere := func(string) (string, error) { return "", os.ErrNotExist }

	if got, from := findTestease("/opt/te", self, onPath); got != "/opt/te" || from != "--testease" {
		t.Errorf("flag: %q from %q", got, from)
	}
	if got, from := findTestease("", self, onPath); got != "/usr/local/bin/testease" || from != "PATH" {
		t.Errorf("path: %q from %q", got, from)
	}
	beside := filepath.Join(dir, name)
	if err := os.WriteFile(beside, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, from := findTestease("", self, onPath); got != beside || from != "beside this app" {
		t.Errorf("beside: %q from %q", got, from)
	}
	if got, _ := findTestease("", filepath.Join(t.TempDir(), "app"), nowhere); got != "" {
		t.Errorf("nothing found reads as %q", got)
	}
}
