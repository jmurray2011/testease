package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/record"
)

func importCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <run.tsv>",
		Short: "read a run.tsv written before testease into a record a judge can read",
		Long: "Import a run.tsv written by an older script. The imported record keeps\n" +
			"the original values, including -1 for an unparseable value and ? for a\n" +
			"value the instrument did not recognise.",
		Args: cobra.ExactArgs(1),
		RunE: doImport,
	}
	cmd.Flags().String("run-id", "", "the id the run was published under; the directory name by default")
	cmd.Flags().String("scenario", "", "the scenario it came from")
	cmd.Flags().String("host", "", "what to record for rows that name no host")
	return cmd
}

func doImport(cmd *cobra.Command, args []string) error {
	source := args[0]
	f, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	defer func() { _ = f.Close() }()

	runID := flagString(cmd, "run-id")
	if runID == "" {
		// The id the corpus already cites. A run that changed its id on being
		// imported would break every finding that named it.
		runID = filepath.Base(filepath.Dir(source))
		if runID == "." || runID == ".." || runID == string(filepath.Separator) {
			// A bare filename has no directory to take an id from, and "."
			// would write the record into the runs directory root itself.
			return fmt.Errorf("cannot derive a run id from %s: name it with --run-id, or import the file from its run directory", source)
		}
	}
	dir := filepath.Join(flagString(cmd, "runs-dir"), runID)
	if _, err := os.Stat(filepath.Join(dir, record.EventsFile)); err == nil {
		return fmt.Errorf("%s already holds a record; importing would append a second reading of the same rows", dir)
	}

	if err := record.Import(dir, f, record.ImportOptions{
		RunID:    runID,
		Scenario: flagString(cmd, "scenario"),
		Host:     flagString(cmd, "host"),
	}); err != nil {
		return err
	}

	rec, err := record.Read(dir)
	if err != nil {
		return err
	}
	if err := record.WriteSummary(dir, record.Summarize(rec)); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), runID)
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "imported %s into %s\n", source, dir)
	return nil
}
