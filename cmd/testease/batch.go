package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/batch"
	"github.com/jmurray2011/testease/internal/campaign"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/targetcatalog"
)

func batchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "run independent scenarios with bounded concurrency",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	cmd.AddCommand(batchValidateCmd(), batchRunCmd(), batchStatusCmd())
	return cmd
}

func batchValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <batch.yaml>",
		Short: "validate every scenario in a batch without starting targets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			definition, err := batch.Load(args[0])
			if err != nil {
				return &runner.PreconditionError{Message: err.Error()}
			}
			if err := validateBatch(definition, flagString(cmd, "scenario-root")); err != nil {
				return &runner.PreconditionError{Message: err.Error()}
			}
			if flagString(cmd, "output") == "json" {
				return writeJSON(cmd, map[string]any{"batch": definition.Name, "tests": len(definition.Tests), "valid": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "valid batch %s (%d scenarios)\n", definition.Name, len(definition.Tests))
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func validateBatch(definition *batch.Definition, scenarioRoot string) error {
	for _, test := range definition.Tests {
		manifest, err := scenario.Load(filepath.Join(scenarioRoot, test.Scenario))
		if err != nil {
			return fmt.Errorf("scenario %q: %w", test.Scenario, err)
		}
		params := maps.Clone(test.Params)
		if test.Target != "" {
			catalog, err := targetcatalog.Load(test.TargetCatalog)
			if err != nil {
				return fmt.Errorf("scenario %q: %w", test.Scenario, err)
			}
			selection, err := catalog.Resolve(targetcatalog.Selector{Name: test.Target})
			if err != nil {
				return fmt.Errorf("scenario %q: %w", test.Scenario, err)
			}
			params = maps.Clone(selection.Profile.Params)
			maps.Copy(params, test.Params)
		}
		if err := validateCampaignManifestResolution(manifest, &campaign.Definition{Params: params}); err != nil {
			return fmt.Errorf("scenario %q: %w", test.Scenario, err)
		}
	}
	return nil
}

func batchRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <batch.yaml>",
		Short: "run every scenario in a batch, up to its parallel limit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stop, err := withSignals(cmd)
			if err != nil {
				return err
			}
			defer stop()
			definition, err := batch.Load(args[0])
			if err != nil {
				return &runner.PreconditionError{Message: err.Error()}
			}
			if err := validateBatch(definition, flagString(cmd, "scenario-root")); err != nil {
				return &runner.PreconditionError{Message: err.Error()}
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			tool := toolRef()
			result, err := batch.Run(cmd.Context(), batch.Options{
				Definition: definition, RunsDir: flagString(cmd, "runs-dir"),
				ScenarioRoot: flagString(cmd, "scenario-root"), Invoker: campaign.ExecInvoker{Executable: executable},
				Progress: cmd.ErrOrStderr(), ToolVersion: tool.Version, ToolCommit: tool.Commit,
			})
			if err != nil {
				return err
			}
			if flagString(cmd, "output") == "json" {
				if err := writeJSON(cmd, result); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d scenario(s)\t%s\n", result.ID, result.Status, len(result.Tests), result.Dir)
				for _, test := range result.Tests {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\t%s\n", test.Scenario, test.Status, test.RunID)
				}
			}
			if result.ExitCode != 0 {
				return &batchExitError{ID: result.ID, Code: result.ExitCode}
			}
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func batchStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <batch-id>",
		Short: "show the saved status of each batch scenario",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := filepath.Join(flagString(cmd, "runs-dir"), "batches", args[0], "batch.json")
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var result batch.Result
			if err := json.Unmarshal(body, &result); err != nil {
				return err
			}
			if flagString(cmd, "output") == "json" {
				return writeJSON(cmd, result)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", result.ID, result.Status)
			for _, test := range result.Tests {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\t%s\n", test.Scenario, test.Status, test.RunID)
			}
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

type batchExitError struct {
	ID   string
	Code int
}

func (e *batchExitError) Error() string {
	return fmt.Sprintf("batch %s finished with exit code %d", e.ID, e.Code)
}

func codeFromBatch(err error) (int, bool) {
	var batchErr *batchExitError
	if errors.As(err, &batchErr) {
		return batchErr.Code, true
	}
	return 0, false
}
