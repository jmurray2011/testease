package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
)

// progressReporter is a live rendering of events, never another record. It
// prints only selected lifecycle fields; dumping arbitrary payloads here would
// eventually dump a value a scenario meant to keep out of terminal logs.
type progressReporter struct {
	mu        sync.Mutex
	out       io.Writer
	mode      string
	heartbeat time.Duration
	active    map[string]activeStep
	media     map[string]activeMedia
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}

type activeStep struct {
	runID string
	cell  string
	step  int
	total int
	label string
	since time.Time
}

type activeMedia struct {
	runID string
	cell  string
	host  string
	item  int
	total int
	name  string
	bytes int64
	since time.Time
}

type progressLine struct {
	Event     string  `json:"event"`
	RunID     string  `json:"run_id"`
	Cell      string  `json:"cell,omitempty"`
	Step      int     `json:"step,omitempty"`
	Total     int     `json:"total,omitempty"`
	Label     string  `json:"label,omitempty"`
	Name      string  `json:"name,omitempty"`
	Host      string  `json:"host,omitempty"`
	Item      int     `json:"item,omitempty"`
	Bytes     int64   `json:"bytes,omitempty"`
	Status    string  `json:"status,omitempty"`
	RC        *int    `json:"rc,omitempty"`
	Seconds   float64 `json:"seconds,omitempty"`
	Heartbeat bool    `json:"heartbeat,omitempty"`
}

func newProgressReporter(out io.Writer, mode string, heartbeat time.Duration) *progressReporter {
	p := &progressReporter{
		out: out, mode: mode, heartbeat: heartbeat,
		active: map[string]activeStep{}, media: map[string]activeMedia{},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if mode == "off" || heartbeat <= 0 {
		close(p.done)
		return p
	}
	go p.heartbeats()
	return p
}

func (p *progressReporter) Close() {
	p.once.Do(func() {
		if p.mode != "off" && p.heartbeat > 0 {
			close(p.stop)
		}
		<-p.done
	})
}

func (p *progressReporter) Observe(e runner.ProgressEvent) {
	if p.mode == "off" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	switch e.Kind {
	case record.KindRunStarted:
		p.write(progressLine{Event: "run-started", RunID: e.RunID, Cell: e.Cell})

	case record.KindTargetResolved:
		resolved, ok := e.Data.(record.TargetResolved)
		if ok {
			p.write(progressLine{Event: "target-resolved", RunID: e.RunID, Cell: e.Cell, Host: resolved.Host})
		}

	case record.KindMediaStarted:
		started, ok := e.Data.(record.MediaStageStarted)
		if !ok {
			return
		}
		active := activeMedia{
			runID: e.RunID, cell: e.Cell, host: started.Host,
			item: started.Item, total: started.Total, name: started.Name,
			bytes: started.Size, since: time.Now(),
		}
		p.media[mediaProgressKey(e, started.Host, started.Item)] = active
		p.write(progressLine{
			Event: "media-stage-started", RunID: e.RunID, Cell: e.Cell,
			Host: started.Host, Item: started.Item, Total: started.Total,
			Name: started.Name, Bytes: started.Size,
		})

	case record.KindMediaStaged:
		staged, ok := e.Data.(record.MediaStaged)
		if !ok {
			return
		}
		delete(p.media, mediaProgressKey(e, staged.Host, staged.Item))
		p.write(progressLine{
			Event: "media-staged", RunID: e.RunID, Cell: e.Cell,
			Host: staged.Host, Item: staged.Item, Total: staged.Total,
			Name: staged.Name, Bytes: staged.Size, Seconds: staged.DurationS,
		})

	case record.KindStepStarted:
		started, ok := e.Data.(record.StepStarted)
		if !ok {
			return
		}
		active := activeStep{
			runID: e.RunID, cell: e.Cell, step: e.Step, total: e.Total,
			label: started.Label, since: time.Now(),
		}
		p.active[progressKey(e)] = active
		p.write(progressLine{
			Event: "step-started", RunID: e.RunID, Cell: e.Cell,
			Step: e.Step, Total: e.Total, Label: started.Label, Host: started.Host,
		})

	case record.KindOperation:
		op, ok := e.Data.(record.Operation)
		if !ok {
			return
		}
		delete(p.active, progressKey(e))
		rc := op.RC
		p.write(progressLine{
			Event: "step-measured", RunID: e.RunID, Cell: e.Cell,
			Step: e.Step, Total: e.Total, Status: string(op.Status), RC: &rc, Seconds: op.DurationS,
		})

	case record.KindCellStarted:
		cell, ok := e.Data.(record.CellStarted)
		if ok {
			p.write(progressLine{Event: "cell-started", RunID: e.RunID, Cell: cell.Key, Total: e.Total})
		}

	case record.KindCellFinished:
		cell, ok := e.Data.(record.CellFinished)
		if ok {
			p.write(progressLine{Event: "cell-finished", RunID: e.RunID, Cell: cell.Key, Total: e.Total, Status: string(cell.Status)})
		}

	case record.KindRunFinished:
		finished, ok := e.Data.(record.RunFinished)
		if ok {
			p.write(progressLine{Event: "run-finished", RunID: e.RunID, Cell: e.Cell, Status: string(finished.Status), Seconds: finished.DurationS})
		}
	}
}

func progressKey(e runner.ProgressEvent) string {
	return fmt.Sprintf("%s\x00%s\x00%d", e.RunID, e.Cell, e.Step)
}

func mediaProgressKey(e runner.ProgressEvent, host string, item int) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", e.RunID, e.Cell, host, item)
}

func (p *progressReporter) heartbeats() {
	ticker := time.NewTicker(p.heartbeat)
	defer ticker.Stop()
	defer close(p.done)
	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			for _, step := range p.active {
				p.write(progressLine{
					Event: "step-running", RunID: step.runID, Cell: step.cell,
					Step: step.step, Total: step.total, Label: step.label,
					Seconds: time.Since(step.since).Seconds(), Heartbeat: true,
				})
			}
			for _, media := range p.media {
				p.write(progressLine{
					Event: "media-stage-running", RunID: media.runID, Cell: media.cell,
					Host: media.host, Item: media.item, Total: media.total,
					Name: media.name, Bytes: media.bytes,
					Seconds: time.Since(media.since).Seconds(), Heartbeat: true,
				})
			}
			p.mu.Unlock()
		case <-p.stop:
			return
		}
	}
}

func (p *progressReporter) write(line progressLine) {
	if p.mode == "json" {
		_ = json.NewEncoder(p.out).Encode(line)
		return
	}

	where := ""
	if line.Cell != "" {
		where = "cell " + line.Cell + " "
	}
	step := ""
	if line.Step > 0 {
		step = fmt.Sprintf("[%d/%d] ", line.Step, line.Total)
	}
	switch line.Event {
	case "run-started":
		_, _ = fmt.Fprintf(p.out, "run %s %sstarted\n", line.RunID, where)
	case "target-resolved":
		_, _ = fmt.Fprintf(p.out, "%starget %s resolved\n", where, line.Host)
	case "media-stage-started":
		_, _ = fmt.Fprintf(p.out, "%sstaging media %d/%d on %s: %s\n",
			where, line.Item, line.Total, line.Host, mediaDescription(line.Name, line.Bytes))
	case "media-stage-running":
		_, _ = fmt.Fprintf(p.out, "%sstill staging media %d/%d after %.0fs on %s: %s\n",
			where, line.Item, line.Total, line.Seconds, line.Host, mediaDescription(line.Name, line.Bytes))
	case "media-staged":
		_, _ = fmt.Fprintf(p.out, "%sstaged media %d/%d on %s in %.1fs: %s\n",
			where, line.Item, line.Total, line.Host, line.Seconds, mediaDescription(line.Name, line.Bytes))
	case "step-started":
		_, _ = fmt.Fprintf(p.out, "%s%sstarting: %s\n", where, step, line.Label)
	case "step-running":
		_, _ = fmt.Fprintf(p.out, "%s%sstill running after %.0fs: %s\n", where, step, line.Seconds, line.Label)
	case "step-measured":
		_, _ = fmt.Fprintf(p.out, "%s%s%s rc=%d in %.1fs\n", where, step, line.Status, *line.RC, line.Seconds)
	case "cell-started":
		_, _ = fmt.Fprintf(p.out, "cell %s started\n", line.Cell)
	case "cell-finished":
		_, _ = fmt.Fprintf(p.out, "cell %s %s\n", line.Cell, line.Status)
	case "run-finished":
		_, _ = fmt.Fprintf(p.out, "run %s %sfinished %s in %.1fs\n", line.RunID, where, line.Status, line.Seconds)
	}
}

func mediaDescription(name string, bytes int64) string {
	if bytes <= 0 {
		return name
	}
	const unit = int64(1024)
	if bytes < unit {
		return fmt.Sprintf("%s (%d B)", name, bytes)
	}
	value := float64(bytes)
	suffix := "KiB"
	value /= float64(unit)
	if value >= float64(unit) {
		value /= float64(unit)
		suffix = "MiB"
	}
	if value >= float64(unit) {
		value /= float64(unit)
		suffix = "GiB"
	}
	return fmt.Sprintf("%s (%.1f %s)", name, value, suffix)
}
