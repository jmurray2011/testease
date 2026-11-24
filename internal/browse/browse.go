// Package browse reads a runs directory for what a person looking through it
// needs: which records there are, and what one of them holds.
//
// It is a projection like any other. Every answer is read off the record's own
// events, the attempt that stands, the same way the judges and views read it,
// and nothing here concludes anything. Status is what the run measured. A
// verdict is only ever the file a judge wrote, shown with whether the bytes it
// judged are still the bytes on disk.
package browse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// Unreadable is the status of a record whose events would not read. It is
// listed rather than skipped: a listing that silently passed one over would be
// the shortest way to lose evidence.
const Unreadable = "unreadable"

// Run is one record, as much of it as a listing needs.
type Run struct {
	RunID    string    `json:"run"`
	Scenario string    `json:"scenario"`
	Steps    int       `json:"steps"`
	Cells    int       `json:"cells,omitempty"`
	Status   string    `json:"status"`
	Dir      string    `json:"dir"`
	Targets  []Target  `json:"targets,omitempty"`
	Verdicts []Verdict `json:"verdicts,omitempty"`
}

// Target is what became of one host's target: the last disposition the runner
// recorded for it. An empty fate means none was recorded, which is itself
// worth seeing on a run that did not finish.
type Target struct {
	Host string `json:"host"`
	Ref  string `json:"ref,omitempty"`
	Fate string `json:"fate,omitempty"`
}

// Verdict is one verdict file, as written. Current says whether the events it
// judged are the events on disk now; one that is not current is not this
// record's verdict, and a reader must not show it as though it were.
type Verdict struct {
	Judge    string    `json:"judge"`
	Version  string    `json:"version"`
	Verdict  string    `json:"verdict"`
	JudgedAt time.Time `json:"judged_at"`
	Current  bool      `json:"current"`
}

// List reads every record directly under dir, sorted by run id - scenario then
// timestamp, so a scenario's records fall into the order they were taken. A
// directory with no events file is not a record and is passed over.
func List(dir string) ([]Run, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read runs directory: %w", err)
	}

	runs := make([]Run, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(path, record.EventsFile)); err != nil {
			continue
		}
		runs = append(runs, read(path).Run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RunID < runs[j].RunID })
	return runs, nil
}

// Detail is one record opened: everything a person reads it for.
type Detail struct {
	Run

	Params     map[string]string `json:"params,omitempty"`
	Tool       record.ToolRef    `json:"tool"`
	Runner     record.HostRef    `json:"runner"`
	EventsHash string            `json:"events_hash"`

	Hosts    []record.TargetResolved `json:"hosts,omitempty"`
	StepList []Step                  `json:"step_list,omitempty"`

	// Reports are what the reader had to tolerate; Warnings are what the run
	// itself recorded about values it repaired at the parse boundary. Neither
	// is hidden, because a reader that hides them is hiding the defect.
	Reports  []record.Report  `json:"reports,omitempty"`
	Warnings []record.Warning `json:"warnings,omitempty"`
}

// Step is one step of the attempt that stands. RC and Status are absent until
// the step reported: a step that started and never came back has no outcome,
// and must not borrow one.
type Step struct {
	Seq       int           `json:"seq"`
	Label     string        `json:"label"`
	Host      string        `json:"host"`
	Probe     string        `json:"probe,omitempty"`
	Status    string        `json:"status,omitempty"`
	RC        *int          `json:"rc,omitempty"`
	DurationS float64       `json:"duration_s,omitempty"`
	Stdout    string        `json:"stdout,omitempty"`
	Stderr    string        `json:"stderr,omitempty"`
	Claim     string        `json:"claim,omitempty"`
	Facts     []record.Fact `json:"facts,omitempty"`
}

// Open reads one record in full.
func Open(dir string) (Detail, error) {
	if _, err := os.Stat(filepath.Join(dir, record.EventsFile)); err != nil {
		return Detail{}, fmt.Errorf("open record: %w", err)
	}
	return read(dir), nil
}

func read(dir string) Detail {
	d := Detail{Run: Run{RunID: filepath.Base(dir), Dir: dir, Status: Unreadable}}

	rec, err := record.Read(dir)
	if err != nil {
		return d
	}
	if body, err := os.ReadFile(filepath.Join(dir, record.EventsFile)); err == nil {
		d.EventsHash = record.HashBytes(body)
	}
	d.Reports = rec.Reports
	d.Status = string(record.StatusUnfinished)

	// The attempt that stands, the same one every projection reads.
	targets := map[string]int{}
	steps := map[int]int{}
	for _, e := range rec.Latest().Events {
		switch e.Kind {
		case record.KindRunStarted:
			if started, err := record.Decode[record.RunStarted](e); err == nil {
				d.Scenario = started.Scenario.Name
				d.Params = started.Params
				d.Tool = started.Tool
				d.Runner = started.Host
			}
		case record.KindTargetResolved:
			if resolved, err := record.Decode[record.TargetResolved](e); err == nil {
				d.Hosts = append(d.Hosts, resolved)
			}
		case record.KindTargetDisposition:
			if disposition, err := record.Decode[record.TargetDisposition](e); err == nil {
				i, seen := targets[disposition.Host]
				if !seen {
					i = len(d.Targets)
					targets[disposition.Host] = i
					d.Targets = append(d.Targets, Target{Host: disposition.Host})
				}
				d.Targets[i].Ref = disposition.Ref
				d.Targets[i].Fate = disposition.Action
			}
		case record.KindStepStarted:
			d.Steps++
			step := Step{Seq: e.Step}
			if started, err := record.Decode[record.StepStarted](e); err == nil {
				step.Label, step.Host, step.Probe = started.Label, started.Host, started.Probe
			}
			steps[e.Step] = len(d.StepList)
			d.StepList = append(d.StepList, step)
		case record.KindOperation:
			i, ok := steps[e.Step]
			if !ok {
				continue
			}
			if op, err := record.Decode[record.Operation](e); err == nil {
				rc := op.RC
				s := &d.StepList[i]
				s.RC, s.Status, s.DurationS = &rc, string(op.Status), op.DurationS
				s.Stdout, s.Stderr, s.Claim = op.Stdout, op.Stderr, op.Claim.Text
			}
		case record.KindFact:
			i, ok := steps[e.Step]
			if !ok {
				continue
			}
			if fact, err := record.Decode[record.Fact](e); err == nil {
				d.StepList[i].Facts = append(d.StepList[i].Facts, fact)
			}
		case record.KindCellStarted:
			d.Cells++
		case record.KindRecordWarning:
			if warning, err := record.Decode[record.Warning](e); err == nil {
				d.Warnings = append(d.Warnings, warning)
			}
		case record.KindRunFinished:
			if finished, err := record.Decode[record.RunFinished](e); err == nil {
				d.Status = string(finished.Status)
			}
		}
	}

	d.Verdicts = verdicts(dir, d.EventsHash)
	return d
}

// verdicts reads every verdict file the record holds, by judge then version.
// A file that will not parse is passed over: it is not a verdict, and the
// judge that wrote it is the place to find out why.
func verdicts(dir, events string) []Verdict {
	entries, err := os.ReadDir(filepath.Join(dir, judge.VerdictsDir))
	if err != nil {
		return nil
	}
	var found []Verdict
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, judge.VerdictsDir, e.Name()))
		if err != nil {
			continue
		}
		var v judge.Verdict
		if err := json.Unmarshal(body, &v); err != nil || v.Judge == "" {
			continue
		}
		found = append(found, Verdict{
			Judge:    v.Judge,
			Version:  v.Version,
			Verdict:  v.Verdict,
			JudgedAt: v.JudgedAt,
			Current:  events != "" && v.Evidence.Events == events,
		})
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Judge != found[j].Judge {
			return found[i].Judge < found[j].Judge
		}
		return found[i].Version < found[j].Version
	})
	return found
}
