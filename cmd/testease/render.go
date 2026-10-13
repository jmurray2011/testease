package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/render"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
)

func renderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render <run-id>...",
		Short: "render a record; the record itself is events.jsonl",
		Args:  cobra.MinimumNArgs(1),
		RunE:  doRender,
	}
	cmd.Flags().String("view", "tsv", "tsv, table, rows for a census, facts across several runs, or meta to rebuild run.json")
	cmd.Flags().String("preset", "", "named view recorded by the scenario; cannot be combined with --view or --fact")
	cmd.Flags().StringP("output", "o", "text", "text, json or html")
	cmd.Flags().StringArray("fact", nil,
		"show only these fact keys (repeatable, or comma-separated); a key nothing recorded renders absent")
	return cmd
}

func doRender(cmd *cobra.Command, args []string) error {
	view, _ := cmd.Flags().GetString("view")
	output := flagString(cmd, "output")
	facts, err := factKeys(cmd)
	if err != nil {
		return err
	}
	preset := flagString(cmd, "preset")
	if preset != "" && (cmd.Flags().Changed("view") || cmd.Flags().Changed("fact")) {
		return fmt.Errorf("--preset selects both the view and its facts; it cannot be combined with --view or --fact")
	}
	if preset == "" {
		if err := validateRenderRequest(view, output, facts, len(args)); err != nil {
			return err
		}
	}

	recs := make([]*record.Record, 0, len(args))
	for _, arg := range args {
		rec, err := readRun(cmd, arg)
		if err != nil {
			return err
		}
		recs = append(recs, rec)
	}
	if preset != "" {
		view, facts, err = recordedView(recs, preset)
		if err != nil {
			return err
		}
		if err := validateRenderRequest(view, output, facts, len(args)); err != nil {
			return err
		}
	}

	if view == "facts" {
		switch output {
		case "json":
			doc, err := render.ProjectFactsOf(recs, facts)
			if err != nil {
				return err
			}
			return render.DocumentJSON(cmd.OutOrStdout(), doc)
		case "html":
			doc, err := render.ProjectFactsOf(recs, facts)
			if err != nil {
				return err
			}
			return render.HTML(cmd.OutOrStdout(), doc, pageTitle(view, args), pageFooter(args))
		default:
			return render.FactsOf(cmd.OutOrStdout(), cmd.ErrOrStderr(), recs, facts)
		}
	}

	rec := recs[0]

	// meta is already the record's own object, so json is what it writes either
	// way; wrapping it would be a second shape for the same thing.
	if output == "json" && view != "meta" {
		doc, err := render.ProjectFiltered(rec, view, facts)
		if err != nil {
			return err
		}
		return render.DocumentJSON(cmd.OutOrStdout(), doc)
	}

	if output == "html" {
		doc, err := render.ProjectFiltered(rec, view, facts)
		if err != nil {
			return err
		}
		return render.HTML(cmd.OutOrStdout(), doc, pageTitle(view, args), pageFooter(args))
	}

	// Data on stdout, everything the view has to say about the record on
	// stderr. A notice among the rows would be parsed as a row.
	switch view {
	case "tsv":
		return render.TSV(cmd.OutOrStdout(), cmd.ErrOrStderr(), rec)
	case "table":
		return render.Table(cmd.OutOrStdout(), cmd.ErrOrStderr(), rec)
	case "rows":
		return render.RowsOf(cmd.OutOrStdout(), cmd.ErrOrStderr(), rec, facts)
	case "meta":
		return render.Meta(cmd.OutOrStdout(), cmd.ErrOrStderr(), rec)
	default:
		return fmt.Errorf("no such view %q; there is tsv, table, rows, facts and meta", view)
	}
}

func validateRenderRequest(view, output string, facts []string, records int) error {
	// facts is the only view that reads across records, so it is the only one
	// more than one run id means anything to. Rendering the first and dropping
	// the rest would answer a question nobody asked.
	if view != "facts" && records > 1 {
		return fmt.Errorf("%s renders one record; only the facts view reads across several", view)
	}

	// meta rebuilds run.json. There is no table in it to lay out, so asking for
	// a page and being handed that file would be a silently different answer.
	if view == "meta" && output == "html" {
		return fmt.Errorf("the meta view rebuilds run.json; there is no page to make of it")
	}

	// A narrowing nothing can act on is a flag that did nothing, and silence
	// there reads as a table that had those columns.
	if len(facts) > 0 && view != "rows" && view != "facts" {
		return fmt.Errorf("--fact narrows the readings a view shows, and %s shows none; it is for rows and facts", view)
	}
	return nil
}

func recordedView(recs []*record.Record, name string) (string, []string, error) {
	var selected *record.ViewRef
	for _, rec := range recs {
		var found *record.ViewRef
		for _, event := range rec.Latest().Events {
			if event.Kind != record.KindRunStarted {
				continue
			}
			started, err := record.Decode[record.RunStarted](event)
			if err != nil {
				return "", nil, err
			}
			if view, ok := started.Views[name]; ok {
				copy := view
				found = &copy
			}
		}
		if found == nil {
			return "", nil, fmt.Errorf("record %s declares no view preset %q", filepath.Base(rec.Dir), name)
		}
		if selected == nil {
			selected = found
			continue
		}
		if selected.Kind != found.Kind || !slices.Equal(selected.Facts, found.Facts) {
			return "", nil, fmt.Errorf("record %s declares a different %q view", filepath.Base(rec.Dir), name)
		}
	}
	return selected.Kind, slices.Clone(selected.Facts), nil
}

// pageTitle names the page after what it renders. One record answers to its run
// id; several answer to none, so the page says how many rather than picking one.
func pageTitle(view string, args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return fmt.Sprintf("%d records, %s", len(args), view)
}

// pageFooter names the records the page was made from, so a page that outlives
// the terminal it was made in still says what it is of.
func pageFooter(args []string) string {
	return strings.Join(args, "  ")
}

// readRun resolves a run id the way every read command does: a directory that
// holds a record, or a name under the runs directory.
func readRun(cmd *cobra.Command, arg string) (*record.Record, error) {
	return record.Read(recordDir(cmd, arg))
}

func scenariosCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scenarios",
		Short: "list the scenarios under the scenario root",
		Args:  cobra.NoArgs,
		RunE:  doScenarios,
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doScenarios(cmd *cobra.Command, _ []string) error {
	root := flagString(cmd, "scenario-root")
	names, err := scenario.Names(root)
	if err != nil {
		// The one error path a newcomer meets first, and it used to be a bare
		// errno. Naming the flag and the example tree is the same courtesy
		// every other refusal in this tool extends.
		if errors.Is(err, os.ErrNotExist) {
			return &runner.PreconditionError{
				Message: fmt.Sprintf("no scenario tree at %s", root),
				Remedy:  "testease scenarios --scenario-root <tree>   # or examples, which ships with this tool",
			}
		}
		return err
	}

	if flagString(cmd, "output") == "json" {
		if names == nil {
			names = []string{}
		}
		return writeJSON(cmd, struct {
			Root      string   `json:"root"`
			Scenarios []string `json:"scenarios"`
		}{Root: root, Scenarios: names})
	}

	if len(names) == 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no scenarios under %s\n", root)
		return nil
	}
	for _, n := range names {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), n)
	}
	return nil
}

// writeJSON is the one encoder every read command uses, so -o json means the
// same thing on all of them.
func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// factKeys is which readings the reader asked for. Repeatable and
// comma-separated both work, because a person types one and a script writes
// the other.
func factKeys(cmd *cobra.Command) ([]string, error) {
	given, err := cmd.Flags().GetStringArray("fact")
	if err != nil {
		return nil, nil
	}
	var keys []string
	for _, item := range given {
		for _, key := range strings.Split(item, ",") {
			if key = strings.TrimSpace(key); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}
