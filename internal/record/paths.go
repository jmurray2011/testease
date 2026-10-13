package record

import (
	"fmt"
	"strings"
	"time"
)

// EventsFile is the evidence, and the only file in a record that must exist.
const EventsFile = "events.jsonl"

// SummaryFile is a cacheable summary of a record, regenerable from the events.
// It is not the record: after a hard process death it is absent or stale while
// the events are complete up to the crash.
const SummaryFile = "run.json"

// The subdirectories a record carries. cells/ is created on demand by matrix
// mode; a single-target run has none.
const (
	StepsDir     = "steps"
	ArtifactsDir = "artifacts"
	VerdictsDir  = "verdicts"
	CellsDir     = "cells"
)

const stampLayout = "20060102T150405"

// RunID builds the id of a run. The shape is a contract: the prefix carries
// the scenario, the stamp is UTC seconds, and ids sort, grep, and cite as
// opaque keys. A bare timestamp is a corpus defect.
func RunID(scenario string, at time.Time) string {
	return scenario + "-" + at.UTC().Format(stampLayout) + "Z"
}

// StepSlug names a step from its sequence number and label: every
// non-alphanumeric run collapses to a single dash, and the result is trimmed
// and prefixed with the zero-padded sequence.
func StepSlug(seq int, label string) string {
	var b strings.Builder
	dashed := false
	for _, r := range label {
		if isAlphanumeric(r) {
			b.WriteRune(r)
			dashed = false
			continue
		}
		if !dashed {
			b.WriteByte('-')
			dashed = true
		}
	}
	return fmt.Sprintf("%02d-%s", seq, strings.Trim(b.String(), "-"))
}

// StepPaths returns a step's stdout and stderr paths, relative to the record
// root and always slash-separated - they are recorded, and a record is read on
// machines that did not write it.
func StepPaths(seq int, label string) (stdout, stderr string) {
	slug := StepSlug(seq, label)
	return StepsDir + "/" + slug + ".out", StepsDir + "/" + slug + ".err"
}

func isAlphanumeric(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return false
	}
}
