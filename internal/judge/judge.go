// Package judge reads a record and reaches a conclusion about it.
//
// Judges are the only thing in testease that reaches conclusions, and they are
// pure functions from evidence to verdict. That is what makes interpretation
// replayable: when a judge turns out to have been wrong - and three withdrawn
// findings in the corpus were a judge bug, not a measurement bug - the record
// is re-judged rather than re-measured.
//
// This package imports record and nothing else internal. It never writes to a
// record's evidence, only alongside it.
package judge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/record"
)

// Schema is the verdict file's schema version.
const Schema = 1

// VerdictsDir is where verdicts live inside a record: derived, appended,
// never edited.
const VerdictsDir = "verdicts"

// A Judge turns evidence into a conclusion. It is pure: same record, same
// version, same answer, with no clock and no I/O of its own.
type Judge interface {
	Name() string
	Version() string
	Judge(rec *record.Record) (Result, error)
}

// Result is what a judge concluded, before the envelope is put around it.
type Result struct {
	// Verdict is the judge's own vocabulary. Nothing outside the judge
	// interprets this string except by comparing it to that judge's pass set.
	Verdict string

	// Steps is the per-step detail for judges whose unit is a step. Matrix
	// judges use Cells instead; a reader takes whichever is present.
	Steps map[string]StepVerdict
	Cells map[string]StepVerdict

	// Unjudged names what the judge would not reach a conclusion about, and
	// why. A judge never manufactures a verdict for unmeasured evidence.
	Unjudged map[string]string

	// CellEvidence pins the bytes of each cell a census judge read, so that a
	// verdict over many records can be checked against all of them.
	CellEvidence map[string]string

	// ExtraEvidence pins anything else a judge had to read to reach its
	// answer. A judge whose conclusion depended on something outside the
	// record - a baseline it compared against - has to say which something, or
	// the verdict cannot be checked later.
	ExtraEvidence map[string]string

	// RecordEvidence pins every top-level record a multi-record judge read.
	RecordEvidence map[string]string
}

// StepVerdict is one subject's conclusion and the reasons for it.
type StepVerdict struct {
	Verdict string   `json:"verdict"`
	Because []string `json:"because,omitempty"`
}

// Verdict is a Result with its envelope: which judge, over which bytes, when.
type Verdict struct {
	Schema  int    `json:"schema"`
	Judge   string `json:"judge"`
	Version string `json:"version"`

	// JudgedAt is envelope, not verdict. A wall clock inside the conclusion
	// would make every judge impure, so this is excluded when one verdict is
	// compared with another.
	JudgedAt time.Time `json:"judged_at"`

	// Evidence pins the exact bytes judged, so a verdict can be checked
	// against the record it claims to have read.
	Evidence Evidence `json:"evidence"`

	Verdict  string                 `json:"verdict"`
	Steps    map[string]StepVerdict `json:"steps,omitempty"`
	Cells    map[string]StepVerdict `json:"cells,omitempty"`
	Unjudged map[string]string      `json:"unjudged,omitempty"`
}

// Evidence is what a verdict was reached from.
type Evidence struct {
	Events  string            `json:"events"`
	Cells   map[string]string `json:"cells,omitempty"`
	Extra   map[string]string `json:"extra,omitempty"`
	Records map[string]string `json:"records,omitempty"`
}

// NothingToJudgeError says the judge had nothing to work with. It is not a
// pass: a record that declared no expectations has not met any, and saying so
// is the difference between a checked run and an unchecked one.
type NothingToJudgeError struct {
	Judge    string
	Unjudged map[string]string
}

func (e *NothingToJudgeError) Error() string {
	if len(e.Unjudged) == 0 {
		return fmt.Sprintf("%s has nothing to judge: this record declares no expectations", e.Judge)
	}
	return fmt.Sprintf("%s has nothing to judge: every expectation belongs to a step that was not measured (%s)",
		e.Judge, describe(e.Unjudged))
}

// DisagreesError says a judge reached a different conclusion than the one
// already stored under the same name and version.
//
// There are only two ways that happens, and both are serious: the judge is not
// a pure function of the record, or the record moved under it. Either makes
// every finding that cited this file unreproducible, so the file is left as it
// is and the disagreement is reported.
type DisagreesError struct {
	Path    string
	Judge   string
	Version string
}

func (e *DisagreesError) Error() string {
	return fmt.Sprintf(
		"%s already holds a different verdict for %s@%s: either that judge is not a pure "+
			"function of the record, or the record changed under it. If the judge's conclusions "+
			"changed on purpose, it needs a new version rather than a rewrite of this file",
		e.Path, e.Judge, e.Version,
	)
}

// UnknownJudgeError names a judge testease does not ship, and the ones it does.
type UnknownJudgeError struct {
	Name  string
	Known []string
}

func (e *UnknownJudgeError) Error() string {
	return fmt.Sprintf("no judge named %q; testease ships %s", e.Name, strings.Join(e.Known, ", "))
}

// shipped is a list, not a registry. A new judge is a code change, which is
// the whole abstraction budget this design allows itself.
func shipped() []Judge {
	return []Judge{Expect{}, Matrix{}, RestoreMatrix{}, ReleaseReadiness{}, WindowsUpgradeReadiness{}, Baseline{}, Compare{}}
}

// ByName finds a shipped judge.
func ByName(name string) (Judge, error) {
	known := make([]string, 0, len(shipped()))
	for _, j := range shipped() {
		if j.Name() == name {
			return j, nil
		}
		known = append(known, j.Name()+"@"+j.Version())
	}
	return nil, &UnknownJudgeError{Name: name, Known: known}
}

// Apply runs a judge over a record and puts the envelope around its answer.
func Apply(j Judge, rec *record.Record) (Verdict, error) {
	result, err := j.Judge(rec)
	if err != nil {
		return Verdict{}, err
	}

	events, err := os.ReadFile(filepath.Join(rec.Dir, record.EventsFile))
	if err != nil {
		return Verdict{}, fmt.Errorf("hash the evidence: %w", err)
	}

	return Verdict{
		Schema:   Schema,
		Judge:    j.Name(),
		Version:  j.Version(),
		JudgedAt: time.Now().UTC(),
		Evidence: Evidence{
			Events:  HashBytes(events),
			Cells:   result.CellEvidence,
			Extra:   result.ExtraEvidence,
			Records: result.RecordEvidence,
		},
		Verdict:  result.Verdict,
		Steps:    result.Steps,
		Cells:    result.Cells,
		Unjudged: result.Unjudged,
	}, nil
}

// HashBytes is how evidence is pinned.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Outcome is what saving a verdict did.
type Outcome int

// A verdict is written once. Re-judging either reproduces it exactly or the
// judge is not pure, and there is no third possibility that ends in an edit.
const (
	Written Outcome = iota
	Reproduced
)

func (o Outcome) String() string {
	if o == Reproduced {
		return "reproduced"
	}
	return "written"
}

// Save writes a verdict beside the record it judged.
//
// An existing verdict for the same judge and version is never overwritten. If
// the new one matches it, nothing is written and the outcome says so. If it
// does not, that is refused: either the judge is not pure or the evidence
// moved, and both make every finding that cited this file unreproducible.
func Save(dir string, v Verdict) (Outcome, error) {
	if err := os.MkdirAll(filepath.Join(dir, VerdictsDir), 0o755); err != nil {
		return Written, fmt.Errorf("make room for verdicts: %w", err)
	}
	path := filepath.Join(dir, VerdictsDir, v.Judge+"@"+v.Version+".json")

	body, err := encode(v)
	if err != nil {
		return Written, err
	}

	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		same, err := sameVerdict(existing, body)
		if err != nil {
			return Written, err
		}
		if !same {
			return Written, &DisagreesError{Path: path, Judge: v.Judge, Version: v.Version}
		}
		return Reproduced, nil

	case errors.Is(err, os.ErrNotExist):
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return Written, fmt.Errorf("write verdict: %w", err)
		}
		return Written, nil

	default:
		return Written, fmt.Errorf("read verdict: %w", err)
	}
}

// Load reads a verdict file back.
func Load(dir, name, version string) (Verdict, error) {
	path := filepath.Join(dir, VerdictsDir, name+"@"+version+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, fmt.Errorf("read verdict: %w", err)
	}
	var v Verdict
	if err := json.Unmarshal(body, &v); err != nil {
		return Verdict{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}

func encode(v Verdict) ([]byte, error) {
	// Indented and newline-terminated: a verdict ships with a published record
	// and is read by people, with jq at most.
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode verdict: %w", err)
	}
	return append(body, '\n'), nil
}

// sameVerdict compares two verdicts as conclusions, ignoring when they were
// reached.
func sameVerdict(a, b []byte) (bool, error) {
	stripped := make([][]byte, 0, 2)
	for _, body := range [][]byte{a, b} {
		var v Verdict
		if err := json.Unmarshal(body, &v); err != nil {
			return false, fmt.Errorf("compare verdicts: %w", err)
		}
		v.JudgedAt = time.Time{}
		out, err := json.Marshal(v)
		if err != nil {
			return false, fmt.Errorf("compare verdicts: %w", err)
		}
		stripped = append(stripped, out)
	}
	return bytes.Equal(stripped[0], stripped[1]), nil
}

func describe(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
