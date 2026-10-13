package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Summary is run.json: a cacheable view of a record, and never the record.
//
// After a hard process death events.jsonl is complete up to the crash and this
// is absent or stale. Every consumer reads the events and may use this only as
// a cache it can rebuild; code that treats it as the record and the events as
// an implementation detail is wrong by definition.
//
// It carries no verdict, ever. A verdict here would be a second semantic
// artifact that can disagree with the verdict file a finding cites, and the
// record has to be self-contained as evidence rather than as a conclusion.
type Summary struct {
	Schema int    `json:"schema"`
	RunID  string `json:"run_id"`

	Scenario ScenarioRef       `json:"scenario"`
	Tool     ToolRef           `json:"tool"`
	Driver   string            `json:"driver"`
	Params   map[string]string `json:"params,omitempty"`
	Matrix   *Matrix           `json:"matrix,omitempty"`
	Host     HostRef           `json:"host"`

	// Targets keep all three identity layers. A summary that flattened them
	// would lose the difference between what was asked for and what answered.
	Targets []TargetResolved `json:"targets,omitempty"`

	// Status is the runner's axis, rolled up. Steps and Cells count what
	// reached each status, so a census can say it closed over cells nobody
	// measured rather than reading as though it covered them.
	Status Status         `json:"status"`
	Steps  map[Status]int `json:"steps,omitempty"`
	Cells  map[Status]int `json:"cells,omitempty"`

	DurationS float64 `json:"duration_s"`

	// Artifacts is what the run kept, by path within the record.
	Artifacts []string `json:"artifacts,omitempty"`

	// EventsHash pins the bytes this was built from. A hash that does not
	// match the events file is how a reader detects a stale summary, which is
	// the whole reason this may be trusted as a cache at all.
	EventsHash string `json:"events_hash"`
}

// Summarize rebuilds a record's summary from its events.
func Summarize(rec *Record) Summary {
	s := Summary{Schema: Schema, Status: StatusInterrupted}
	steps := map[int]Status{}

	for _, e := range rec.Events {
		switch e.Kind {
		case KindRunStarted:
			started, err := Decode[RunStarted](e)
			if err != nil {
				continue
			}
			// A resumed census appends a second run-started; the later one is
			// the current lineage, and the earlier one is still in the events.
			s.RunID = started.RunID
			s.Scenario = started.Scenario
			s.Tool = started.Tool
			s.Driver = started.Driver
			s.Params = started.Params
			s.Matrix = started.Matrix
			s.Host = started.Host

		case KindTargetResolved:
			resolved, err := Decode[TargetResolved](e)
			if err != nil {
				continue
			}
			s.Targets = append(s.Targets, resolved)

		case KindOperation:
			op, err := Decode[Operation](e)
			if err != nil {
				continue
			}
			steps[e.Step] = op.Status

		case KindCellFinished:
			finished, err := Decode[CellFinished](e)
			if err != nil {
				continue
			}
			if s.Cells == nil {
				s.Cells = map[Status]int{}
			}
			s.Cells[finished.Status]++

		case KindRunFinished:
			finished, err := Decode[RunFinished](e)
			if err != nil {
				continue
			}
			s.Status = finished.Status
			s.DurationS = finished.DurationS
			if len(finished.Cells) > 0 {
				s.Cells = finished.Cells
			}
		}
	}

	for _, status := range steps {
		if s.Steps == nil {
			s.Steps = map[Status]int{}
		}
		s.Steps[status]++
	}

	s.Artifacts = artifactsIn(rec.Dir)
	if body, err := os.ReadFile(filepath.Join(rec.Dir, EventsFile)); err == nil {
		s.EventsHash = HashBytes(body)
	}
	return s
}

// HashBytes is how a summary pins the events it was built from.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EncodeSummary renders a summary. Same events, same bytes: a cache that
// regenerated differently every time would be no use for detecting staleness.
func EncodeSummary(s Summary) ([]byte, error) {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode summary: %w", err)
	}
	return append(body, '\n'), nil
}

// WriteSummary writes run.json beside the events it summarises.
func WriteSummary(dir string, s Summary) error {
	body, err := EncodeSummary(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, SummaryFile), body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", SummaryFile, err)
	}
	return nil
}

func artifactsIn(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, ArtifactsDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, ArtifactsDir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out
}
