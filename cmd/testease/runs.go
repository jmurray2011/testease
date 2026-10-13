package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/record"
)

func runsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "the records under the runs directory, newest last",
		Long: "Scenarios and standing targets can both be listed; the records could only be\n" +
			"found with ls. This reads each one's own events for what it is, rather than\n" +
			"parsing its name: a run id carries a scenario and a timestamp by contract, and\n" +
			"a directory somebody renamed still answers for itself.",
		Args: cobra.NoArgs,
		RunE: doRuns,
	}
	cmd.Flags().String("scenario", "", "only records of this scenario")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

// runEntry is one record, as much of it as a listing needs.
type runEntry struct {
	RunID    string `json:"run"`
	Scenario string `json:"scenario"`
	Steps    int    `json:"steps"`
	Cells    int    `json:"cells,omitempty"`
	Status   string `json:"status"`
	Dir      string `json:"dir"`
}

func doRuns(cmd *cobra.Command, _ []string) error {
	dir := flagString(cmd, "runs-dir")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no runs directory at %s\n", dir)
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}

	only := flagString(cmd, "scenario")
	found := make([]runEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		entry, ok := readEntry(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		if only != "" && entry.Scenario != only {
			continue
		}
		found = append(found, entry)
	}
	// By run id, which is scenario then an ISO timestamp - so a scenario's
	// records fall into the order they were taken.
	sort.Slice(found, func(i, j int) bool { return found[i].RunID < found[j].RunID })

	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, struct {
			Root string     `json:"root"`
			Runs []runEntry `json:"runs"`
		}{Root: dir, Runs: found})
	}

	if len(found) == 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no records under %s\n", dir)
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "run\tscenario\tsteps\tcells\tstatus"); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	for _, r := range found {
		cells := "-"
		if r.Cells > 0 {
			cells = fmt.Sprint(r.Cells)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n",
			r.RunID, r.Scenario, r.Steps, cells, r.Status); err != nil {
			return fmt.Errorf("list runs: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	return nil
}

// readEntry reads one record for what it says about itself. A directory that
// holds no events file is not a record and is passed over rather than guessed
// at; a record that will not parse is listed as unreadable, because a listing
// that silently skipped one would be the shortest way to lose evidence.
func readEntry(dir string) (runEntry, bool) {
	if _, err := os.Stat(filepath.Join(dir, record.EventsFile)); err != nil {
		return runEntry{}, false
	}
	entry := runEntry{RunID: filepath.Base(dir), Dir: dir, Status: "unreadable"}

	rec, err := record.Read(dir)
	if err != nil {
		return entry, true
	}

	// The attempt that stands, the same one every projection reads.
	latest := rec.Latest()
	for _, e := range latest.Events {
		switch e.Kind {
		case record.KindRunStarted:
			if started, err := record.Decode[record.RunStarted](e); err == nil {
				entry.Scenario = started.Scenario.Name
			}
		case record.KindStepStarted:
			entry.Steps++
		case record.KindCellStarted:
			entry.Cells++
		}
	}

	entry.Status = string(record.StatusUnfinished)
	for _, e := range latest.Events {
		if e.Kind != record.KindRunFinished {
			continue
		}
		if finished, err := record.Decode[record.RunFinished](e); err == nil {
			entry.Status = string(finished.Status)
		}
	}
	return entry, true
}
