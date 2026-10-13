package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LockedError says another writer holds this record, and names it. One writer
// per file is what lets a reader trust that sequence numbers mean anything.
type LockedError struct {
	Dir string
	PID int
}

func (e *LockedError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("record %s is held by pid %d", e.Dir, e.PID)
	}
	return fmt.Sprintf("record %s is held by another writer", e.Dir)
}

// Writer appends events to a record. It owns the sequence numbers: the bash
// took them from the caller and inherited every caller's bugs, and gaps and
// duplicates are defects rather than choices.
//
// A Writer holds an exclusive lock on the events file for its life. Callers
// must Close it.
type Writer struct {
	dir string

	mu  sync.Mutex
	f   *os.File
	seq int

	// pendingNL says the file ends in a torn line - a writer died mid-event -
	// so the next append starts with a newline. The torn bytes stay exactly
	// where they are; without the separator the next event would be glued onto
	// them and both would be lost as one invalid line.
	pendingNL bool
}

// Open creates a record's layout and takes the writer's lock on its events
// file. Reopening a record that already has events continues its sequence.
func Open(dir string) (*Writer, error) {
	for _, sub := range []string{StepsDir, ArtifactsDir, VerdictsDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create record layout: %w", err)
		}
	}

	path := filepath.Join(dir, EventsFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := lockExclusive(f); err != nil {
		pid := ownerPID(dir)
		_ = f.Close()
		return nil, &LockedError{Dir: dir, PID: pid}
	}

	seq, err := lastSeq(dir)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	torn, err := tornTail(path)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Writer{dir: dir, f: f, seq: seq, pendingNL: torn}, nil
}

// tornTail reports whether the events file ends mid-line.
func tornTail(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() == 0 {
		return false, nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return last[0] != '\n', nil
}

// Emit appends one event and returns the sequence number it was given. Pass
// step 0 for run-level events.
func (w *Writer) Emit(kind Kind, step int, data any) (int, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return 0, fmt.Errorf("marshal %s payload: %w", kind, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.seq++
	line, err := json.Marshal(Event{
		Schema: Schema,
		Seq:    w.seq,
		TS:     time.Now().UTC().Truncate(time.Millisecond),
		Kind:   kind,
		Step:   step,
		Data:   payload,
	})
	if err != nil {
		return w.seq, fmt.Errorf("marshal %s envelope: %w", kind, err)
	}
	if w.pendingNL {
		line = append([]byte{'\n'}, line...)
	}
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		return w.seq, fmt.Errorf("append to %s: %w", filepath.Join(w.dir, EventsFile), err)
	}
	w.pendingNL = false
	return w.seq, nil
}

// Warn records what a Boundary repaired, as record-warning events pointing at
// the sequence number of the event the repairs belong to. Passing no warnings
// writes nothing.
func (w *Writer) Warn(aboutSeq, step int, warnings ...Warning) error {
	for _, warning := range warnings {
		warning.About = fmt.Sprintf("seq %d %s", aboutSeq, warning.About)
		if _, err := w.Emit(KindRecordWarning, step, warning); err != nil {
			return err
		}
	}
	return nil
}

// Sync flushes the events file to disk. The runner calls it at step
// boundaries, where the cost is paid once per probe rather than per event.
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Join(w.dir, EventsFile), err)
	}
	return nil
}

// Close flushes and releases the writer's lock.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f == nil {
		return nil
	}
	syncErr := w.f.Sync()
	closeErr := w.f.Close()
	w.f = nil

	if syncErr != nil {
		return fmt.Errorf("sync record: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close record: %w", closeErr)
	}
	return nil
}

// lastSeq is the highest sequence number already in a record, so that
// reopening one continues its sequence instead of restarting it.
func lastSeq(dir string) (int, error) {
	rec, err := Read(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	high := 0
	for _, e := range rec.Events {
		if e.Seq > high {
			high = e.Seq
		}
	}
	return high, nil
}

// ownerPID reports the pid a record's run-started event named, so a second
// writer can say who holds it. Zero means the record has not said yet.
func ownerPID(dir string) int {
	rec, err := Read(dir)
	if err != nil {
		return 0
	}
	for _, e := range rec.Events {
		if e.Kind != KindRunStarted {
			continue
		}
		started, err := Decode[RunStarted](e)
		if err != nil {
			return 0
		}
		return started.Host.PID
	}
	return 0
}
