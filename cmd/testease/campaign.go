package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/campaign"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/targetcatalog"
)

func campaignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "campaign",
		Short: "run and inspect a recorded multi-run campaign",
		Args:  cobra.NoArgs,
	}
	cmd.PersistentFlags().String("campaign-root", envOr("TESTEASE_CAMPAIGN_ROOT", "campaigns"), "directory holding campaign directories")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	cmd.AddCommand(campaignRunCmd(), campaignStatusCmd(), campaignValidateCmd())
	return cmd
}

func campaignValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <campaign>",
		Short: "check a campaign against its scenario without running hooks",
		Args:  cobra.ExactArgs(1),
		RunE:  doCampaignValidate,
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doCampaignValidate(cmd *cobra.Command, args []string) error {
	definition, err := campaign.Load(filepath.Join(flagString(cmd, "campaign-root"), args[0]))
	if err != nil {
		return campaignValidationError(cmd, err.Error(), "")
	}
	manifest, err := scenario.Load(filepath.Join(flagString(cmd, "scenario-root"), definition.Scenario))
	if err != nil {
		return campaignValidationError(cmd, err.Error(), "testease scenarios --scenario-root "+flagString(cmd, "scenario-root"))
	}
	if err := definition.ValidateAgainstScenario(manifest); err != nil {
		return campaignValidationError(cmd, err.Error(), "testease validate "+definition.Scenario+" --scenario-root "+flagString(cmd, "scenario-root"))
	}
	if err := validateCampaignManifestResolution(manifest, definition); err != nil {
		return campaignValidationError(cmd, err.Error(), "testease validate "+definition.Scenario+" --scenario-root "+flagString(cmd, "scenario-root"))
	}
	if strings.EqualFold(flagString(cmd, "output"), "json") {
		return writeJSON(cmd, campaignValidationResult{
			Campaign: definition.Name,
			Scenario: manifest.Name,
			Valid:    true,
		})
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "valid campaign %s\n", definition.Name)
	return nil
}

type campaignValidationResult struct {
	Campaign string `json:"campaign,omitempty"`
	Scenario string `json:"scenario,omitempty"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
	Remedy   string `json:"remedy,omitempty"`
}

func campaignValidationError(cmd *cobra.Command, message, remedy string) error {
	if strings.EqualFold(flagString(cmd, "output"), "json") {
		if err := writeJSON(cmd, campaignValidationResult{Valid: false, Error: message, Remedy: remedy}); err != nil {
			return err
		}
	}
	return &runner.PreconditionError{Message: message, Remedy: remedy}
}

func validateCampaignManifestResolution(manifest *scenario.Manifest, definition *campaign.Definition) error {
	var axes map[string][]string
	var err error
	if len(definition.Matrix) > 0 {
		axes = maps.Clone(definition.Matrix)
	} else {
		axes, err = manifest.Axes()
		if err != nil {
			return err
		}
	}
	for name := range definition.Params {
		delete(axes, name)
	}
	if len(axes) == 0 {
		_, err := manifest.Resolve(definition.Params)
		return err
	}
	cells, err := (runner.Scope{Axes: axes, Exclude: manifest.Matrix.Exclude, Source: "campaign-validate"}).Cells()
	if err != nil {
		return err
	}
	for _, cell := range cells {
		values := maps.Clone(definition.Params)
		maps.Copy(values, cell.Params)
		if _, err := manifest.Resolve(values); err != nil {
			return err
		}
	}
	return nil
}

func campaignRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <campaign>",
		Short: "run and inspect a group of related runs",
		Args:  cobra.ExactArgs(1),
		RunE:  doCampaignRun,
	}
	cmd.Flags().Bool("keep", false, "leave campaign-owned resources standing and record that choice")
	cmd.Flags().Int("repeat", 0, "override the campaign repetition count")
	cmd.Flags().StringArray("param", nil, "override a campaign scenario parameter, k=v (repeatable)")
	cmd.Flags().StringArray("matrix", nil, "override a campaign matrix axis, k=v1,v2 (repeatable)")
	cmd.Flags().Int("par", 0, "override how many matrix cells may run at once")
	cmd.Flags().String("target", "", "override the campaign's target name")
	cmd.Flags().StringArray("target-tag", nil, "add a required target tag (repeatable)")
	cmd.Flags().String("target-catalog", os.Getenv("TESTEASE_TARGET_CATALOG"), "target catalog YAML")
	cmd.Flags().String("input-bundle", os.Getenv("TESTEASE_INPUT_BUNDLE"), "verified input bundle for each child run")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doCampaignRun(cmd *cobra.Command, args []string) error {
	stop, err := withSignals(cmd)
	if err != nil {
		return err
	}
	defer stop()
	definition, err := campaign.Load(filepath.Join(flagString(cmd, "campaign-root"), args[0]))
	if err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}
	if cmd.Flags().Changed("repeat") {
		repeat, _ := cmd.Flags().GetInt("repeat")
		if repeat < 1 {
			return &runner.PreconditionError{Message: "--repeat must be at least 1"}
		}
		if definition.Reproduce && repeat < 2 {
			return &runner.PreconditionError{Message: "this campaign asks for reproduction, so --repeat must be at least 2"}
		}
		definition.Repeat = repeat
	}
	if err := applyCampaignScopeOverrides(cmd, definition); err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}
	manifest, err := scenario.Load(filepath.Join(flagString(cmd, "scenario-root"), definition.Scenario))
	if err != nil {
		return &runner.PreconditionError{
			Message: err.Error(),
			Remedy:  "testease validate " + definition.Scenario + " --scenario-root " + flagString(cmd, "scenario-root"),
		}
	}
	if err := definition.ValidateAgainstScenario(manifest); err != nil {
		return &runner.PreconditionError{
			Message: err.Error(),
			Remedy:  "testease plan " + definition.Scenario + " --scenario-root " + flagString(cmd, "scenario-root"),
		}
	}
	if err := validateCampaignManifestResolution(manifest, definition); err != nil {
		return &runner.PreconditionError{
			Message: err.Error(),
			Remedy:  "testease validate " + definition.Scenario + " --scenario-root " + flagString(cmd, "scenario-root"),
		}
	}

	selectorName := definition.Target.Name
	if cmd.Flags().Changed("target") {
		selectorName = flagString(cmd, "target")
	}
	selectorTags := append([]string{}, definition.Target.Tags...)
	extraTags, err := cmd.Flags().GetStringArray("target-tag")
	if err != nil {
		return err
	}
	selectorTags = append(selectorTags, extraTags...)
	resolvedName, resolvedTags, catalogHash, err := resolveCampaignTarget(flagString(cmd, "target-catalog"), selectorName, selectorTags)
	if err != nil {
		return err
	}

	inputBundle := flagString(cmd, "input-bundle")
	if inputBundle == "" {
		inputBundle = definition.InputBundle
	}
	keep, _ := cmd.Flags().GetBool("keep")
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate testease executable: %w", err)
	}
	result, err := campaign.Run(cmd.Context(), campaign.Options{
		Definition: definition, RunsDir: flagString(cmd, "runs-dir"), ScenarioRoot: flagString(cmd, "scenario-root"),
		TargetCatalog: flagString(cmd, "target-catalog"), CatalogSHA256: catalogHash,
		TargetName: resolvedName, TargetTags: resolvedTags, SelectorName: selectorName, SelectorTags: selectorTags,
		InputBundle: inputBundle, Keep: keep, Invoker: campaign.ExecInvoker{Executable: executable},
		ToolVersion: version, ToolCommit: commit,
		OnStart: func(id, dir string) {
			if strings.EqualFold(flagString(cmd, "output"), "json") {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "campaign %s started; follow: testease follow %s\n", id, id)
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), id)
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "follow: testease follow %s\nrecord: %s\n", id, dir)
			}
		},
	})
	if err != nil {
		return err
	}
	if strings.EqualFold(flagString(cmd, "output"), "json") {
		if err := writeJSON(cmd, result); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "campaign %s finished %s; %d run(s); cleanup %s\n", result.ID, result.Status, len(result.Runs), result.Cleanup)
	}
	if result.ExitCode != 0 {
		return &campaignExitError{id: result.ID, status: result.Status, code: result.ExitCode}
	}
	return nil
}

func applyCampaignScopeOverrides(cmd *cobra.Command, definition *campaign.Definition) error {
	paramOverrides, err := params(cmd)
	if err != nil {
		return err
	}
	matrixOverrides, err := campaignMatrixOverrides(cmd)
	if err != nil {
		return err
	}
	for name := range paramOverrides {
		if _, alsoSwept := matrixOverrides[name]; alsoSwept {
			return fmt.Errorf("campaign override both pins and sweeps parameter %q", name)
		}
	}
	definition.Params = maps.Clone(definition.Params)
	definition.Matrix = maps.Clone(definition.Matrix)
	if definition.Params == nil {
		definition.Params = map[string]string{}
	}
	if definition.Matrix == nil {
		definition.Matrix = map[string][]string{}
	}
	for name, value := range paramOverrides {
		definition.Params[name] = value
		delete(definition.Matrix, name)
	}
	for name, values := range matrixOverrides {
		definition.Matrix[name] = values
		delete(definition.Params, name)
	}
	if cmd.Flags().Changed("par") {
		parallel, _ := cmd.Flags().GetInt("par")
		if parallel < 1 {
			return fmt.Errorf("--par must be at least 1")
		}
		definition.Parallel = parallel
	}
	return nil
}

func campaignMatrixOverrides(cmd *cobra.Command) (map[string][]string, error) {
	pairs, err := cmd.Flags().GetStringArray("matrix")
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(pairs))
	for _, pair := range pairs {
		name, raw, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("--matrix %q is not k=v1,v2", pair)
		}
		values := strings.Split(raw, ",")
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
			if values[i] == "" {
				return nil, fmt.Errorf("--matrix %q contains an empty value", pair)
			}
		}
		out[strings.TrimSpace(name)] = values
	}
	return out, nil
}

func resolveCampaignTarget(path, name string, tags []string) (string, []string, string, error) {
	if name == "" && len(tags) == 0 {
		return "", nil, "", nil
	}
	if path == "" {
		return "", nil, "", &runner.PreconditionError{Message: "the campaign selects a target, but no target catalog was given", Remedy: "use --target-catalog <path> or set TESTEASE_TARGET_CATALOG"}
	}
	catalog, err := targetcatalog.Load(path)
	if err != nil {
		return "", nil, "", &runner.PreconditionError{Message: err.Error()}
	}
	selection, err := catalog.Resolve(targetcatalog.Selector{Name: name, Tags: tags})
	if err != nil {
		return "", nil, "", &runner.PreconditionError{Message: err.Error(), Remedy: "testease targets catalog --target-catalog " + path}
	}
	return selection.Profile.Name, selection.Profile.Tags, selection.CatalogSHA256, nil
}

func campaignStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <campaign-id>",
		Short: "show what a recorded campaign is doing or how it finished",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := campaignRecordDir(cmd, args[0])
			status, err := campaign.ReadStatus(dir)
			if err != nil {
				return err
			}
			if strings.EqualFold(flagString(cmd, "output"), "json") {
				return writeJSON(cmd, status)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d run(s)\tcleanup %s\n", status.ID, status.Status, len(status.Runs), or(status.Cleanup, "pending"))
			if status.Current != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "current: %s\n", status.Current)
			}
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func followCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "follow <campaign-id>",
		Short: "show campaign progress",
		Args:  cobra.ExactArgs(1),
		RunE:  doFollow,
	}
	cmd.Flags().Bool("no-wait", false, "print what exists and return")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doFollow(cmd *cobra.Command, args []string) error {
	dir := campaignRecordDir(cmd, args[0])
	noWait, _ := cmd.Flags().GetBool("no-wait")
	if strings.EqualFold(flagString(cmd, "output"), "json") {
		return followEvents(cmd.Context(), cmd.OutOrStdout(), dir, noWait)
	}
	return followFile(cmd.Context(), cmd.OutOrStdout(), filepath.Join(dir, "output.log"), dir, noWait)
}

func followFile(ctx context.Context, out io.Writer, path, campaignDir string, noWait bool) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open campaign output: %w", err)
	}
	defer func() { _ = file.Close() }()
	for {
		if _, err := io.Copy(out, file); err != nil {
			return err
		}
		if noWait || campaignDone(campaignDir) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func followEvents(ctx context.Context, out io.Writer, dir string, noWait bool) error {
	written := 0
	encoder := json.NewEncoder(out)
	for {
		events, err := campaign.ReadEvents(dir)
		if err != nil {
			return err
		}
		for _, event := range events[written:] {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		written = len(events)
		if noWait || campaignDone(dir) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func campaignDone(dir string) bool {
	status, err := campaign.ReadStatus(dir)
	return err == nil && status.Status != campaign.StatusRunning
}

func campaignRecordDir(cmd *cobra.Command, arg string) string {
	if _, err := os.Stat(filepath.Join(arg, campaign.EventsFile)); err == nil {
		return arg
	}
	return filepath.Join(flagString(cmd, "runs-dir"), "campaigns", arg)
}

type campaignExitError struct {
	id, status string
	code       int
}

func (e *campaignExitError) Error() string {
	return fmt.Sprintf("campaign %s finished %s", e.id, e.status)
}

func (e *campaignExitError) ExitCode() int { return e.code }

func codeFromCampaign(err error) (int, bool) {
	var campaignErr *campaignExitError
	if errors.As(err, &campaignErr) {
		return campaignErr.ExitCode(), true
	}
	return 0, false
}
