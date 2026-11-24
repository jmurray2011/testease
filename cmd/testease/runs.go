package main

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/browse"
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

func doRuns(cmd *cobra.Command, _ []string) error {
	dir := flagString(cmd, "runs-dir")
	all, err := browse.List(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no runs directory at %s\n", dir)
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}

	only := flagString(cmd, "scenario")
	found := make([]browse.Run, 0, len(all))
	for _, r := range all {
		if only != "" && r.Scenario != only {
			continue
		}
		found = append(found, r)
	}

	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, struct {
			Root string       `json:"root"`
			Runs []browse.Run `json:"runs"`
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
