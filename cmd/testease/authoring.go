package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
)

func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [scenario]",
		Short: "check scenario files without running them",
		Args:  cobra.MaximumNArgs(1),
		RunE:  doValidate,
	}
	cmd.Flags().Bool("all", false, "validate every scenario under the scenario root")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

type validationResult struct {
	Scenario string `json:"scenario"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
}

func doValidate(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	if all && len(args) > 0 {
		return fmt.Errorf("validate accepts a scenario name or --all, not both")
	}
	if !all && len(args) == 0 {
		return fmt.Errorf("validate needs a scenario name or --all")
	}

	root := flagString(cmd, "scenario-root")
	names := args
	if all {
		found, err := scenario.Names(root)
		if err != nil {
			return &runner.PreconditionError{Message: err.Error(), Remedy: "testease scenarios --scenario-root " + root}
		}
		names = found
		if len(names) == 0 {
			return &runner.PreconditionError{
				Message: fmt.Sprintf("no scenarios under %s", root),
				Remedy:  "testease scenarios --scenario-root " + root,
			}
		}
	}

	results := make([]validationResult, 0, len(names))
	valid := true
	for _, name := range names {
		manifest, loadErr := scenario.Load(filepath.Join(root, name))
		err := loadErr
		if err == nil {
			err = validateManifestResolution(manifest)
		}
		result := validationResult{Scenario: name, Valid: err == nil}
		if err != nil {
			result.Error = err.Error()
			valid = false
		}
		results = append(results, result)
	}

	if strings.EqualFold(flagString(cmd, "output"), "json") {
		if err := writeJSON(cmd, struct {
			Valid     bool               `json:"valid"`
			Scenarios []validationResult `json:"scenarios"`
		}{Valid: valid, Scenarios: results}); err != nil {
			return err
		}
	} else {
		for _, result := range results {
			if result.Valid {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "valid scenario %s\n", result.Scenario)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "invalid scenario %s: %s\n", result.Scenario, result.Error)
			}
		}
	}
	if !valid {
		return &runner.PreconditionError{Message: "one or more scenarios are invalid"}
	}
	return nil
}

func validateManifestResolution(manifest *scenario.Manifest) error {
	axes, err := manifest.Axes()
	if err != nil {
		return err
	}
	if len(axes) == 0 {
		_, err := manifest.Resolve(nil)
		return err
	}
	cells, err := (runner.Scope{Axes: axes, Exclude: manifest.Matrix.Exclude, Source: "validate"}).Cells()
	if err != nil {
		return err
	}
	for _, cell := range cells {
		if _, err := manifest.Resolve(cell.Params); err != nil {
			return err
		}
	}
	return nil
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan <scenario>",
		Short: "show resolved steps and matrix cells without running them",
		Args:  cobra.ExactArgs(1),
		RunE:  doPlan,
	}
	cmd.Flags().StringArray("param", nil, "set a scenario parameter, k=v (repeatable)")
	cmd.Flags().StringArray("matrix", nil, "show these axis values, k=v1,v2 (repeatable)")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

type planDocument struct {
	Name     string     `json:"name"`
	Question string     `json:"question,omitempty"`
	Cells    []planCell `json:"cells"`
}

type planCell struct {
	Key    string            `json:"key,omitempty"`
	Params map[string]string `json:"params"`
	Hosts  []planHost        `json:"hosts"`
	Steps  []planStep        `json:"steps"`
}

type planHost struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider,omitempty"`
	Backend  string   `json:"backend,omitempty"`
	Address  string   `json:"address,omitempty"`
	Origin   string   `json:"origin,omitempty"`
	Image    string   `json:"image,omitempty"`
	Media    []string `json:"media,omitempty"`
}

type planStep struct {
	Label       string            `json:"label"`
	Host        string            `json:"host"`
	Probe       string            `json:"probe,omitempty"`
	Carry       *planCarry        `json:"carry,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Media       []string          `json:"media,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
	Fingerprint bool              `json:"fingerprint,omitempty"`
	Expect      map[string]string `json:"expect,omitempty"`
}

type planCarry struct {
	From   string `json:"from"`
	ToHost string `json:"to_host"`
	To     string `json:"to"`
}

func doPlan(cmd *cobra.Command, args []string) error {
	root := flagString(cmd, "scenario-root")
	manifest, err := scenario.Load(filepath.Join(root, args[0]))
	if err != nil {
		return &runner.PreconditionError{Message: err.Error(), Remedy: "testease validate " + args[0] + " --scenario-root " + root}
	}
	pinned, err := params(cmd)
	if err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}
	axes, err := planAxes(cmd, manifest, pinned)
	if err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}

	doc := planDocument{Name: manifest.Name, Question: manifest.Question}
	if len(axes) == 0 {
		resolved, resolveErr := manifest.Resolve(pinned)
		if resolveErr != nil {
			return &runner.PreconditionError{Message: resolveErr.Error()}
		}
		doc.Cells = []planCell{cellDocument("", resolved)}
	} else {
		cells, cellErr := (runner.Scope{Axes: axes, Exclude: manifest.Matrix.Exclude, Source: "plan"}).Cells()
		if cellErr != nil {
			return &runner.PreconditionError{Message: cellErr.Error()}
		}
		for _, cell := range cells {
			values := make(map[string]string, len(pinned)+len(cell.Params))
			for name, value := range pinned {
				values[name] = value
			}
			for name, value := range cell.Params {
				values[name] = value
			}
			resolved, resolveErr := manifest.Resolve(values)
			if resolveErr != nil {
				return &runner.PreconditionError{Message: resolveErr.Error()}
			}
			doc.Cells = append(doc.Cells, cellDocument(cell.Key, resolved))
		}
	}

	if strings.EqualFold(flagString(cmd, "output"), "json") {
		return writeJSON(cmd, doc)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "scenario %s\n", doc.Name)
	if doc.Question != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "question: %s\n", strings.TrimSpace(doc.Question))
	}
	for _, cell := range doc.Cells {
		if cell.Key == "" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "cell: default")
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cell: %s\n", cell.Key)
		}
		for _, step := range cell.Steps {
			operation := or(step.Probe, "carry")
			if step.Carry != nil {
				operation = fmt.Sprintf("carry %s -> %s:%s", step.Carry.From, step.Carry.ToHost, step.Carry.To)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s on %s: %s\n", step.Label, step.Host, operation)
		}
	}
	return nil
}

func planAxes(cmd *cobra.Command, manifest *scenario.Manifest, pinned map[string]string) (map[string][]string, error) {
	pairs, err := cmd.Flags().GetStringArray("matrix")
	if err != nil {
		return nil, err
	}
	if len(pairs) > 0 {
		axes := make(map[string][]string, len(pairs))
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
				if strings.HasPrefix(values[i], "@") {
					return nil, fmt.Errorf("--matrix %q asks for discovery; plan needs explicit discovered values", pair)
				}
			}
			axes[strings.TrimSpace(name)] = values
		}
		return axes, nil
	}
	axes, err := manifest.Axes()
	if err != nil {
		return nil, err
	}
	for name := range pinned {
		delete(axes, name)
	}
	for name, values := range axes {
		if len(values) == 1 && strings.HasPrefix(values[0], "@") {
			return nil, fmt.Errorf("axis %q uses discovery %q; plan needs explicit --matrix values", name, values[0])
		}
	}
	return axes, nil
}

func cellDocument(key string, plan *scenario.Plan) planCell {
	names := make([]string, 0, len(plan.Hosts))
	for name := range plan.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	hosts := make([]planHost, 0, len(names))
	for _, name := range names {
		host := plan.Hosts[name]
		hosts = append(hosts, planHost{
			Name: name, Provider: host.Provider, Backend: host.Backend, Address: host.Address,
			Origin: host.Origin, Image: host.Image, Media: append([]string(nil), host.Media...),
		})
	}
	steps := make([]planStep, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		expect := map[string]string{}
		if step.Expect != nil {
			if step.Expect.RC != nil {
				expect["rc"] = fmt.Sprint(*step.Expect.RC)
			}
			if step.Expect.Fingerprint != "" {
				expect["fingerprint"] = step.Expect.Fingerprint
			}
			for name, value := range step.Expect.Facts {
				expect["fact."+name] = value
			}
		}
		if len(expect) == 0 {
			expect = nil
		}
		var carry *planCarry
		if step.Carry != nil {
			carry = &planCarry{From: step.Carry.From, ToHost: step.Carry.ToHost, To: step.Carry.To}
		}
		steps = append(steps, planStep{
			Label: step.Label, Host: step.Host, Probe: step.Probe, Carry: carry, Args: append([]string(nil), step.Args...),
			Media: append([]string(nil), step.Media...), Timeout: timeDurationString(step.Timeout),
			Fingerprint: step.Fingerprint, Expect: expect,
		})
	}
	return planCell{Key: key, Params: plan.Params, Hosts: hosts, Steps: steps}
}

func timeDurationString(value scenario.Duration) string {
	if value == 0 {
		return ""
	}
	return time.Duration(value).String()
}
