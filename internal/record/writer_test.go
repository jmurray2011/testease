package record_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
)

func openRun(t *testing.T) (*record.Writer, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("gate", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir
}

func TestOpenCreatesTheRecordLayout(t *testing.T) {
	t.Parallel()

	_, dir := openRun(t)

	for _, sub := range []string{"steps", "artifacts", "verdicts"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("stat %s: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}
	// A single-target run has no cells/.
	if _, err := os.Stat(filepath.Join(dir, "cells")); !os.IsNotExist(err) {
		t.Errorf("cells/ exists on a single-target run (err = %v)", err)
	}
}

func TestEmitAllocatesMonotonicSeq(t *testing.T) {
	t.Parallel()

	// The bash took seq from the caller and inherited every caller's bug. The
	// writer owns it: gaps and duplicates are defects, not caller choices.
	w, dir := openRun(t)

	for i := 1; i <= 3; i++ {
		seq, err := w.Emit(record.KindStepStarted, i, record.StepStarted{Label: "s", Host: "alpha"})
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if seq != i {
			t.Errorf("Emit returned seq %d, want %d", seq, i)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(rec.Events))
	}
	for i, e := range rec.Events {
		if e.Seq != i+1 {
			t.Errorf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
		if e.Schema != record.Schema {
			t.Errorf("event %d has schema %d, want %d", i, e.Schema, record.Schema)
		}
		if e.TS.IsZero() {
			t.Errorf("event %d has no timestamp", i)
		}
	}
	if len(rec.Reports) != 0 {
		t.Errorf("a clean record reported %+v", rec.Reports)
	}
}

func TestEventsAccumulateNeverReplace(t *testing.T) {
	t.Parallel()

	w, dir := openRun(t)
	for i := 1; i <= 5; i++ {
		if _, err := w.Emit(record.KindStepStarted, i, record.StepStarted{Label: "s", Host: "alpha"}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if n := len(splitLines(string(raw))); n != 5 {
		t.Errorf("events.jsonl has %d lines, want 5", n)
	}
	if raw[len(raw)-1] != '\n' {
		t.Error("events.jsonl does not end in a newline")
	}
}

func TestReopenAfterATornWriteDoesNotGlueEvents(t *testing.T) {
	t.Parallel()

	w, dir := openRun(t)
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: "gate"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A writer killed mid-Emit leaves a final line with no terminator.
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open events.jsonl: %v", err)
	}
	if _, err := f.WriteString(`{"schema":1,"seq":2,"kind":"opera`); err != nil {
		t.Fatalf("tear the tail: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close events.jsonl: %v", err)
	}

	resumed, err := record.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := resumed.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: "gate"}); err != nil {
		t.Fatalf("Emit after reopen: %v", err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// The torn line stays torn and reported; the resumed attempt's event must
	// not be glued onto it and lost with it.
	if len(rec.Events) != 2 {
		t.Fatalf("events = %d, want the event before the tear and the one after it", len(rec.Events))
	}
	if len(rec.Reports) == 0 {
		t.Error("the torn line went unreported")
	}
	if rec.Latest().Events[0].Kind != record.KindRunStarted {
		t.Error("the resumed attempt's run-started did not survive the reopen")
	}
}

func TestEnvelopeKeepsPayloadsUnderData(t *testing.T) {
	t.Parallel()

	// Envelope fields can never collide with payload fields, so
	// `jq 'select(.kind=="operation") | .data.rc'` always works.
	w, dir := openRun(t)
	if _, err := w.Emit(record.KindOperation, 1, record.Operation{
		RC:     137,
		Status: record.StatusMeasured,
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var envelope struct {
		Schema int             `json:"schema"`
		Seq    int             `json:"seq"`
		TS     string          `json:"ts"`
		Kind   string          `json:"kind"`
		Step   int             `json:"step"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(splitLines(string(raw))[0]), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.Kind != string(record.KindOperation) {
		t.Errorf("kind = %q, want %q", envelope.Kind, record.KindOperation)
	}
	if envelope.Step != 1 {
		t.Errorf("step = %d, want 1", envelope.Step)
	}
	var payload struct {
		RC int `json:"rc"`
	}
	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if payload.RC != 137 {
		t.Errorf("data.rc = %d, want 137", payload.RC)
	}
}

func TestWarnRecordsWhatTheBoundaryRepaired(t *testing.T) {
	t.Parallel()

	w, dir := openRun(t)

	var b record.Boundary
	op := record.Operation{
		RC:        b.Int("operation.rc", "abc"),
		DurationS: b.Seconds("operation.duration_s", "3"),
		Status:    record.StatusMeasured,
	}
	seq, err := w.Emit(record.KindOperation, 1, op)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := w.Warn(seq, 1, b.Warnings()...); err != nil {
		t.Fatalf("Warn: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(rec.Events))
	}
	if rec.Events[1].Kind != record.KindRecordWarning {
		t.Fatalf("second event kind = %q, want %q", rec.Events[1].Kind, record.KindRecordWarning)
	}
	warn, err := record.Decode[record.Warning](rec.Events[1])
	if err != nil {
		t.Fatalf("decode warning: %v", err)
	}
	if want := "seq 1 operation.rc"; warn.About != want {
		t.Errorf("about = %q, want %q", warn.About, want)
	}
	if warn.Recorded != "-1" {
		t.Errorf("recorded = %q, want %q", warn.Recorded, "-1")
	}
}

func TestWarnWithNothingPendingWritesNothing(t *testing.T) {
	t.Parallel()

	w, dir := openRun(t)
	var b record.Boundary
	b.Int("operation.rc", "0")
	if err := w.Warn(1, 1, b.Warnings()...); err != nil {
		t.Fatalf("Warn: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Events) != 0 {
		t.Errorf("events = %d, want 0", len(rec.Events))
	}
}

func TestSecondWriterFailsFastNamingTheOwner(t *testing.T) {
	t.Parallel()

	w, dir := openRun(t)
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID:  filepath.Base(dir),
		Driver: record.DriverManifest,
		Host:   record.HostRef{User: "someone", Hostname: "labhost", PID: os.Getpid()},
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	_, err := record.Open(dir)
	if err == nil {
		t.Fatal("a second writer opened a record another writer holds")
	}
	var locked *record.LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error is %T (%v), want *record.LockedError", err, err)
	}
	if locked.PID != os.Getpid() {
		t.Errorf("LockedError.PID = %d, want %d", locked.PID, os.Getpid())
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
