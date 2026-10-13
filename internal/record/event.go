// Package record is the durable form of a run: an append-only events.jsonl
// that is the evidence, plus the layout around it.
//
// Two rules govern everything here. Nothing under a record is ever rewritten -
// repairs happen at read time and are reported, never applied to disk. And the
// record never invents a value it did not measure: a number that fails to
// parse becomes -1 rather than 0, and an instrument reading nobody recognizes
// becomes "unknown" carrying its raw text rather than being passed through or
// tidied into something legitimate.
//
// This package imports nothing else internal, by design and by lint rule.
package record

import (
	"encoding/json"
	"fmt"
	"time"
)

// Schema is the envelope schema version this package writes. Readers reject
// major versions they do not know rather than guessing at them.
const Schema = 1

// Kind names what an event records.
type Kind string

// The event kinds. Unknown kinds are preserved and reported by readers, never
// dropped and never rewritten into a kind we do know.
//
// record-repair has no producer here, deliberately. This writer is handed typed
// data and repairs nothing: what it cannot read it refuses at the parse
// boundary and says so as a record-warning, and what a reader has to tolerate
// is a read-time Report that never touches the file. The kind stays because a
// record written by something that does repair - an older tool, another
// implementation - has to be readable here rather than reported as a stranger.
const (
	KindRunStarted     Kind = "run-started"
	KindInputResolved  Kind = "input-resolved"
	KindTargetResolved Kind = "target-resolved"
	KindMediaStarted   Kind = "media-stage-started"
	KindMediaStaged    Kind = "media-staged"
	KindStepStarted    Kind = "step-started"
	KindOperation      Kind = "operation"
	KindFact           Kind = "fact"
	KindFingerprint    Kind = "fingerprint"
	KindControlChecked Kind = "control-checked"
	KindCellStarted    Kind = "cell-started"
	KindCellFinished   Kind = "cell-finished"
	KindRunFinished    Kind = "run-finished"
	KindRecordWarning  Kind = "record-warning"
	KindRecordRepair   Kind = "record-repair"
)

var knownKinds = map[Kind]bool{
	KindRunStarted:     true,
	KindInputResolved:  true,
	KindTargetResolved: true,
	KindMediaStarted:   true,
	KindMediaStaged:    true,
	KindStepStarted:    true,
	KindOperation:      true,
	KindFact:           true,
	KindFingerprint:    true,
	KindControlChecked: true,
	KindCellStarted:    true,
	KindCellFinished:   true,
	KindRunFinished:    true,
	KindRecordWarning:  true,
	KindRecordRepair:   true,
}

// Event is the envelope every line of events.jsonl carries. Kind-specific
// fields live under Data so an envelope field can never collide with a payload
// field and jq 'select(.kind=="operation") | .data.rc' always works.
type Event struct {
	Schema int             `json:"schema"`
	Seq    int             `json:"seq"`
	TS     time.Time       `json:"ts"`
	Kind   Kind            `json:"kind"`
	Step   int             `json:"step,omitempty"`
	Data   json.RawMessage `json:"data"`
}

// Decode unmarshals an event's payload. Data stays raw on the Event itself so
// unknown kinds and unknown fields survive a read.
func Decode[T any](e Event) (T, error) {
	var v T
	if err := json.Unmarshal(e.Data, &v); err != nil {
		return v, fmt.Errorf("decode %s payload at seq %d: %w", e.Kind, e.Seq, err)
	}
	return v, nil
}
