package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// logLimit caps how much of testease's own commentary a job keeps on screen.
const logLimit = 256 << 10

// job is one testease command this app started: a run or a doctor. It reads
// --progress json lines off stderr as they come, keeps everything else as the
// command's log, and decodes --output json off stdout once it exits.
type job struct {
	cmd      *exec.Cmd
	onChange func()
	exited   chan struct{}

	mu       sync.Mutex
	steps    []jobStep
	byStep   map[string]int
	log      strings.Builder
	stdout   bytes.Buffer
	runID    string
	status   string
	exit     int
	done     bool
	stopping bool
	err      error
}

// jobStep is one step as the progress lines describe it.
type jobStep struct {
	Cell    string
	Seq     int
	Label   string
	Status  string
	RC      *int
	Seconds float64
	Running bool
}

// jobState is a job as the screen shows it.
type jobState struct {
	Steps    []jobStep
	Log      string
	Stdout   string
	RunID    string
	Status   string
	Exit     int
	Done     bool
	Stopping bool
}

// progressLine is the part of testease's --progress json a screen reads.
type progressLine struct {
	Event   string  `json:"event"`
	RunID   string  `json:"run_id"`
	Cell    string  `json:"cell"`
	Host    string  `json:"host"`
	Name    string  `json:"name"`
	Step    int     `json:"step"`
	Label   string  `json:"label"`
	Status  string  `json:"status"`
	RC      *int    `json:"rc"`
	Seconds float64 `json:"seconds"`
}

// startJob runs testease with args. onChange is called, from the job's own
// goroutine, whenever there is something new to show.
func startJob(exe string, args, env []string, onChange func()) (*job, error) {
	j := &job{onChange: onChange, exited: make(chan struct{}), byStep: map[string]int{}}
	j.cmd = exec.Command(exe, args...) //nolint:gosec // the testease binary the person chose, with flags they typed
	j.cmd.Env = env
	j.cmd.Stdout = &lockedWriter{mu: &j.mu, w: &j.stdout}
	stderr, err := j.cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	prepare(j.cmd)
	if err := j.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start testease: %w", err)
	}
	go j.read(stderr)
	return j, nil
}

func (j *job) read(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		j.line(scanner.Text())
		j.onChange()
	}
	err := j.cmd.Wait()

	j.mu.Lock()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		j.exit = exit.ExitCode()
	default:
		j.exit, j.err = -1, err
		j.appendLog("could not run testease: " + err.Error())
	}
	var result struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(j.stdout.Bytes(), &result) == nil {
		if result.RunID != "" {
			j.runID = result.RunID
		}
		if result.Status != "" {
			j.status = result.Status
		}
		if result.Error != "" {
			j.appendLog(result.Error)
		}
	}
	for i := range j.steps {
		j.steps[i].Running = false
	}
	j.done = true
	j.mu.Unlock()
	close(j.exited)
	j.onChange()
}

// line takes one line of stderr: a progress line if it is one, the command's
// log otherwise.
func (j *job) line(text string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	var p progressLine
	if !strings.HasPrefix(text, "{") || json.Unmarshal([]byte(text), &p) != nil || p.Event == "" {
		j.appendLog(text)
		return
	}
	if p.RunID != "" && p.Cell == "" {
		j.runID = p.RunID
	}
	where := ""
	if p.Cell != "" {
		where = p.Cell + ": "
	}
	switch p.Event {
	case "step-started", "step-running":
		s := j.step(p)
		s.Running = true
		// The label rides step-started; later lines may leave it out.
		if p.Label != "" {
			s.Label = p.Label
		}
		if p.Event == "step-started" {
			j.appendLog(fmt.Sprintf("%sstep %d started: %s", where, p.Step, s.Label))
		}
	case "step-measured":
		s := j.step(p)
		if p.Label != "" {
			s.Label = p.Label
		}
		s.Status, s.RC, s.Seconds, s.Running = p.Status, p.RC, p.Seconds, false
		line := fmt.Sprintf("%sstep %d %s", where, p.Step, orText(p.Status, "finished"))
		if p.RC != nil {
			line += fmt.Sprintf(", rc %d", *p.RC)
		}
		j.appendLog(line + fmt.Sprintf(", %.1f s", p.Seconds))
	case "run-started":
		j.appendLog(where + "run started: " + p.RunID)
	case "target-resolved":
		j.appendLog(where + "target resolved for host " + p.Host)
	case "cell-started":
		j.appendLog(where + "cell started")
	case "cell-finished":
		j.appendLog(where + "cell finished: " + p.Status)
	case "media-stage-started":
		j.appendLog(fmt.Sprintf("%sstaging %s onto %s", where, p.Name, p.Host))
	case "media-staged":
		j.appendLog(fmt.Sprintf("%sstaged %s onto %s, %.1f s", where, p.Name, p.Host, p.Seconds))
	case "run-finished":
		if p.Cell == "" {
			j.status = p.Status
		}
		j.appendLog(where + "run finished: " + p.Status)
	}
}

func (j *job) step(p progressLine) *jobStep {
	key := fmt.Sprintf("%s/%d", p.Cell, p.Step)
	i, ok := j.byStep[key]
	if !ok {
		i = len(j.steps)
		j.byStep[key] = i
		j.steps = append(j.steps, jobStep{Cell: p.Cell, Seq: p.Step})
	}
	return &j.steps[i]
}

func (j *job) appendLog(text string) {
	if j.log.Len() > logLimit {
		return
	}
	j.log.WriteString(text)
	j.log.WriteByte('\n')
	if j.log.Len() > logLimit {
		j.log.WriteString("… the rest of this log is in the record and its output files\n")
	}
}

func (j *job) state() jobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobState{
		Steps: append([]jobStep(nil), j.steps...), Log: j.log.String(), Stdout: j.stdout.String(),
		RunID: j.runID, Status: j.status, Exit: j.exit, Done: j.done, Stopping: j.stopping,
	}
}

// stop asks the command to stop the way Ctrl+C does, so a run stops
// scheduling, lets what is in flight finish and closes its record interrupted.
func (j *job) stop() error {
	j.mu.Lock()
	if j.done {
		j.mu.Unlock()
		return nil
	}
	j.stopping = true
	j.mu.Unlock()
	j.onChange()
	return interrupt(j.cmd)
}

// kill ends the command at once. A run killed leaves its record unfinished and
// its targets wherever they were; it is for a run that will not stop.
func (j *job) kill() error {
	if j.cmd.Process == nil {
		return nil
	}
	return j.cmd.Process.Kill()
}

func (j *job) wait() { <-j.exited }

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// findTestease is the testease binary this app calls: the flag, then one
// beside this app, then the one on PATH. It says which, because two installs
// can be different versions.
func findTestease(flag, self string, lookPath func(string) (string, error)) (path, from string) {
	if flag != "" {
		return flag, "--testease"
	}
	name := "testease"
	if filepath.Ext(self) == ".exe" || os.PathSeparator == '\\' {
		name += ".exe"
	}
	beside := filepath.Join(filepath.Dir(self), name)
	if info, err := os.Stat(beside); err == nil && !info.IsDir() {
		return beside, "beside this app"
	}
	if p, err := lookPath("testease"); err == nil {
		return p, "PATH"
	}
	return "", ""
}
