package main

import (
	"context"
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
	cmd.AddCommand(batchValidateCmd(), batchDoctorCmd(), batchRunCmd(), batchJudgeCmd(), batchStatusCmd())
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "valid batch %s (%d tests)\n", definition.Name, len(definition.Tests))
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
			result, preflight, err := executeBatch(cmd.Context(), batch.Options{
				Definition: definition, RunsDir: flagString(cmd, "runs-dir"),
				ScenarioRoot: flagString(cmd, "scenario-root"), Invoker: campaign.ExecInvoker{Executable: executable},
				Progress: cmd.ErrOrStderr(), ToolVersion: tool.Version, ToolCommit: tool.Commit,
			})
			if err != nil {
				return err
			}
			if preflight.Status != "passed" {
				if err := reportBatchPreflight(cmd, preflight); err != nil {
					return err
				}
				return &runner.PreconditionError{Message: "batch doctor failed", Remedy: "fix the reported checks before running the batch"}
			}
			if flagString(cmd, "output") == "json" {
				if err := writeJSON(cmd, result); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d test(s)\t%s\n", result.ID, result.Status, len(result.Tests), result.Dir)
				for _, test := range result.Tests {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\t%s\n", batchTestDisplay(test), test.Status, test.RunID)
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

// executeBatch keeps the strict preflight before batch.Run, which creates the
// batch record and schedules children. It accepts an invoker so the ordering
// can be verified without starting targets.
func executeBatch(ctx context.Context, opt batch.Options) (batch.Result, batch.PreflightResult, error) {
	preflight := batch.Preflight(ctx, opt, true)
	if preflight.Status != "passed" {
		return batch.Result{}, preflight, nil
	}
	result, err := batch.Run(ctx, opt)
	return result, preflight, err
}

func batchDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor <batch.yaml>",
		Short: "check every batch entry before starting targets",
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
			strict, _ := cmd.Flags().GetBool("strict")
			result := batch.Preflight(cmd.Context(), batch.Options{
				Definition: definition, RunsDir: flagString(cmd, "runs-dir"),
				ScenarioRoot: flagString(cmd, "scenario-root"), Invoker: campaign.ExecInvoker{Executable: executable},
			}, strict)
			if err := reportBatchPreflight(cmd, result); err != nil {
				return err
			}
			if result.Status != "passed" {
				return &runner.PreconditionError{Message: "batch doctor failed", Remedy: "fix the reported checks before running the batch"}
			}
			return nil
		},
	}
	cmd.Flags().Bool("strict", false, "count a warning as a failure")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func reportBatchPreflight(cmd *cobra.Command, result batch.PreflightResult) error {
	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, result)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d test(s)\n", result.Batch, result.Status, len(result.Tests))
	for _, test := range result.Tests {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\t%s\n", test.Name, test.Scenario, test.Status)
		for _, check := range test.Checks {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "    %s\t%s\t%s\n", check.Status, check.Name, check.Found)
			if check.Remedy != "" && check.Status != "ok" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "      try: %s\n", check.Remedy)
			}
		}
		if test.Error != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "    error: %s\n", test.Error)
		}
	}
	return nil
}

func batchJudgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "judge <batch-id>",
		Short: "judge every measured run in a batch and save their verdicts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stop, err := withSignals(cmd)
			if err != nil {
				return err
			}
			defer stop()
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			result, err := batch.Judge(cmd.Context(), flagString(cmd, "runs-dir"), args[0], campaign.ExecInvoker{Executable: executable})
			if err != nil {
				return err
			}
			if flagString(cmd, "output") == "json" {
				if err := writeJSON(cmd, result); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d test(s)\n", result.BatchID, result.Status, len(result.Tests))
				for _, test := range result.Tests {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\n", test.Name, batchJudgmentDisplay(test))
				}
			}
			if result.ExitCode != 0 {
				return &batchExitError{ID: result.BatchID, Code: result.ExitCode}
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
		Short: "show the saved status of each batch test",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runsDir := flagString(cmd, "runs-dir")
			result, err := batch.LoadResult(runsDir, args[0])
			if err != nil {
				return err
			}
			judgment, err := batch.LoadJudgment(runsDir, args[0])
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			hasJudgment := err == nil
			if hasJudgment && len(judgment.Tests) != len(result.Tests) {
				return fmt.Errorf("batch %s judgment has %d entries; measurement has %d", result.ID, len(judgment.Tests), len(result.Tests))
			}
			if hasJudgment {
				for index, test := range result.Tests {
					item := judgment.Tests[index]
					name := test.Name
					if name == "" {
						name = test.Scenario
					}
					if item.Name != name || item.RunID != test.RunID {
						return fmt.Errorf("batch %s judgment entry %d does not match measurement", result.ID, index+1)
					}
				}
			}
			if flagString(cmd, "output") == "json" {
				response := struct {
					batch.Result
					Judgment *batch.Judgment `json:"judgment,omitempty"`
				}{Result: result}
				if hasJudgment {
					response.Judgment = &judgment
				}
				return writeJSON(cmd, response)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", result.ID, result.Status)
			if hasJudgment {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  judgment: %s\n", judgment.Status)
			}
			for index, test := range result.Tests {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\t%s\n", batchTestDisplay(test), test.Status, test.RunID)
				if hasJudgment {
					item := judgment.Tests[index]
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", batchJudgmentDisplay(item))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func batchJudgmentDisplay(test batch.TestJudgment) string {
	if test.Error != "" {
		return fmt.Sprintf("%s: %s", test.Status, test.Error)
	}
	return fmt.Sprintf("%s@%s %s (%s)", test.Judge, test.Version, test.Verdict, test.Status)
}

func batchTestDisplay(test batch.TestResult) string {
	if test.Name != "" && test.Name != test.Scenario {
		return fmt.Sprintf("%s (%s)", test.Name, test.Scenario)
	}
	return test.Scenario
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
