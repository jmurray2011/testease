package main

import (
	"fmt"
	"maps"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/targetcatalog"
)

func addTargetSelectionFlags(cmd *cobra.Command) {
	cmd.Flags().String("target", "", "named target from the target catalog")
	cmd.Flags().StringArray("target-tag", nil, "required target tag (repeatable; every tag must match)")
	cmd.Flags().String("target-catalog", os.Getenv("TESTEASE_TARGET_CATALOG"), "target catalog YAML")
}

func selectedParams(cmd *cobra.Command) (map[string]string, *targetcatalog.Selection, error) {
	overrides, err := params(cmd)
	if err != nil {
		return nil, nil, err
	}
	name := flagString(cmd, "target")
	tags, err := cmd.Flags().GetStringArray("target-tag")
	if err != nil {
		return nil, nil, err
	}
	if name == "" && len(tags) == 0 {
		return overrides, nil, nil
	}
	path := flagString(cmd, "target-catalog")
	if path == "" {
		return nil, nil, &runner.PreconditionError{
			Message: "a target was selected, but no target catalog was given",
			Remedy:  "use --target-catalog <path> or set TESTEASE_TARGET_CATALOG",
		}
	}
	catalog, err := targetcatalog.Load(path)
	if err != nil {
		return nil, nil, &runner.PreconditionError{Message: err.Error()}
	}
	selection, err := catalog.Resolve(targetcatalog.Selector{Name: name, Tags: tags})
	if err != nil {
		return nil, nil, &runner.PreconditionError{
			Message: err.Error(),
			Remedy:  "testease targets catalog --target-catalog " + path,
		}
	}
	merged := maps.Clone(selection.Profile.Params)
	for key, value := range overrides {
		merged[key] = value
	}
	return merged, &selection, nil
}

func targetConditions(selection *targetcatalog.Selection) map[string]string {
	if selection == nil {
		return nil
	}
	return map[string]string{
		"target.catalog_sha256": selection.CatalogSHA256,
		"target.name":           selection.Profile.Name,
		"target.selector":       selectorText(selection.Selector),
		"target.tags":           strings.Join(selection.Profile.Tags, ","),
	}
}

func selectorText(selector targetcatalog.Selector) string {
	var pieces []string
	if selector.Name != "" {
		pieces = append(pieces, "name="+selector.Name)
	}
	if len(selector.Tags) > 0 {
		pieces = append(pieces, "tags="+strings.Join(selector.Tags, ","))
	}
	return strings.Join(pieces, " ")
}

func targetCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "list targets and their tags",
		Args:  cobra.NoArgs,
		RunE:  doTargetCatalog,
	}
	cmd.Flags().String("target", "", "only this target name")
	cmd.Flags().StringArray("tag", nil, "only targets carrying every tag (repeatable)")
	cmd.Flags().String("target-catalog", os.Getenv("TESTEASE_TARGET_CATALOG"), "target catalog YAML")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doTargetCatalog(cmd *cobra.Command, _ []string) error {
	path := flagString(cmd, "target-catalog")
	if path == "" {
		return &runner.PreconditionError{Message: "no target catalog was given", Remedy: "use --target-catalog <path> or set TESTEASE_TARGET_CATALOG"}
	}
	catalog, err := targetcatalog.Load(path)
	if err != nil {
		return &runner.PreconditionError{Message: err.Error()}
	}
	tags, err := cmd.Flags().GetStringArray("tag")
	if err != nil {
		return err
	}
	matches := catalog.Matching(targetcatalog.Selector{Name: flagString(cmd, "target"), Tags: tags})
	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, struct {
			Schema  int                     `json:"schema"`
			SHA256  string                  `json:"sha256"`
			Targets []targetcatalog.Profile `json:"targets"`
		}{Schema: catalog.Schema, SHA256: catalog.SHA256, Targets: matches})
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "name\ttags\tparams"); err != nil {
		return err
	}
	for _, profile := range matches {
		paramNames := make([]string, 0, len(profile.Params))
		for key := range profile.Params {
			paramNames = append(paramNames, key)
		}
		sortStrings(paramNames)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", profile.Name, strings.Join(profile.Tags, ","), strings.Join(paramNames, ",")); err != nil {
			return err
		}
	}
	return tw.Flush()
}
