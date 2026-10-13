package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

func reproduceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reproduce <run-id> <run-id>...",
		Short: "compare stable facts across runs",
		Args:  cobra.MinimumNArgs(2),
		RunE:  doReproduce,
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doReproduce(cmd *cobra.Command, args []string) error {
	recs := make([]*record.Record, 0, len(args))
	for _, arg := range args {
		rec, err := readRun(cmd, arg)
		if err != nil {
			return err
		}
		recs = append(recs, rec)
	}

	verdict, err := judge.ApplyReproduce(recs)
	if err != nil {
		return err
	}
	id, path, outcome, err := judge.SaveComparison(flagString(cmd, "runs-dir"), verdict)
	if err != nil {
		return err
	}

	if flagString(cmd, "output") == "json" {
		if err := writeJSON(cmd, verdict); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s@%s\t%s\n", verdict.Judge, verdict.Version, verdict.Verdict)
		for _, key := range sortedStepNames(verdict.Steps) {
			subject := verdict.Steps[key]
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s\n", subject.Verdict, key)
			for _, because := range subject.Because {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "               %s\n", because)
			}
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "comparison %s %s: %s\n", id, outcome.String(), path)
	}
	if !passed(verdict.Verdict) {
		return &verdictError{verdict: verdict.Verdict, judge: verdict.Judge, version: verdict.Version}
	}
	return nil
}

func sortedStepNames(steps map[string]judge.StepVerdict) []string {
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}
