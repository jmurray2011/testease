package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/reap"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/target"
)

// platformFate requires a matching record before a remote resource can be
// removed. A local PID cannot prove anything about a different runner host.
func platformFate(runsDir, platform string, machine provisioned.Machine) (reap.Fate, string) {
	labels := machine.Labels
	runID, host, cell := labels[target.LabelRun], labels[target.LabelHost], labels[target.LabelCell]
	if labels[target.LabelRunner] == "" || runID == "" || host == "" || machine.ID == "" {
		return reap.Unprovable, "resource lacks a complete testease identity"
	}
	if !safeRecordSegment(runID) || cell != "" && !safeRecordSegment(cell) {
		return reap.Unprovable, "resource labels do not name a safe record path"
	}
	dir := filepath.Join(runsDir, runID)
	if cell != "" {
		dir = filepath.Join(dir, record.CellsDir, cell)
	}
	rec, err := record.Read(dir)
	if err != nil {
		return reap.Unprovable, fmt.Sprintf("no readable run record: %v", err)
	}
	if len(rec.Reports) != 0 {
		return reap.Unprovable, "run record needed read-time repair"
	}
	ref := platform + ":" + machine.ID
	var started, resolved, intent bool
	for _, event := range rec.Events {
		switch event.Kind {
		case record.KindRunStarted:
			item, decodeErr := record.Decode[record.RunStarted](event)
			if decodeErr != nil {
				return reap.Unprovable, decodeErr.Error()
			}
			if item.RunID == runID {
				started = true
			}
		case record.KindTargetResolved:
			item, decodeErr := record.Decode[record.TargetResolved](event)
			if decodeErr != nil {
				return reap.Unprovable, decodeErr.Error()
			}
			if item.Host == host && item.Resolved["ref"] == ref {
				resolved = true
			}
		case record.KindTargetDisposition:
			item, decodeErr := record.Decode[record.TargetDisposition](event)
			if decodeErr != nil {
				return reap.Unprovable, decodeErr.Error()
			}
			if item.Host != host || item.Ref != ref {
				continue
			}
			switch item.Action {
			case "release-intended":
				intent = true
			case "retained", "retain-failed":
				intent = false
			}
		}
	}
	if !started || !resolved {
		return reap.Unprovable, "resource id and run labels do not match a target in the record"
	}
	if !intent {
		return reap.Unprovable, "record does not authorize release of this target"
	}
	return reap.Orphaned, "record contains a matching durable release intent"
}

func safeRecordSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		filepath.Base(value) == value && !strings.ContainsAny(value, `/\`)
}
