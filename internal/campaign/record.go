package campaign

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const EventsFile = "events.jsonl"

type Event struct {
	Schema int             `json:"schema"`
	Seq    int             `json:"seq"`
	TS     time.Time       `json:"ts"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
}

type Writer struct {
	mu   sync.Mutex
	seq  int
	file *os.File
}

func OpenRecord(dir string) (*Writer, error) {
	for _, sub := range []string{"hooks", "runs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create campaign record: %w", err)
		}
	}
	file, err := os.OpenFile(filepath.Join(dir, EventsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open campaign record: %w", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	seq := 0
	if len(events) > 0 {
		seq = events[len(events)-1].Seq
	}
	return &Writer{file: file, seq: seq}, nil
}

func (w *Writer) Emit(kind string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal campaign %s: %w", kind, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	line, err := json.Marshal(Event{Schema: Schema, Seq: w.seq, TS: time.Now().UTC().Truncate(time.Millisecond), Kind: kind, Data: payload})
	if err != nil {
		return fmt.Errorf("marshal campaign event: %w", err)
	}
	if _, err := w.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append campaign event: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync campaign event: %w", err)
	}
	return nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func ReadEvents(dir string) ([]Event, error) {
	file, err := os.Open(filepath.Join(dir, EventsFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open campaign record: %w", err)
	}
	defer func() { _ = file.Close() }()
	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("read campaign event %d: %w", len(events)+1, err)
		}
		if event.Schema != Schema || event.Seq != len(events)+1 || event.Kind == "" {
			return nil, fmt.Errorf("campaign event %d has an invalid envelope", len(events)+1)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan campaign record: %w", err)
	}
	return events, nil
}

func Decode[T any](event Event) (T, error) {
	var value T
	if err := json.Unmarshal(event.Data, &value); err != nil {
		return value, fmt.Errorf("decode campaign %s: %w", event.Kind, err)
	}
	return value, nil
}

func ID(name string, at time.Time) string {
	return name + "-" + at.UTC().Format("20060102T150405") + "Z"
}
