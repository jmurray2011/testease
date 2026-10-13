package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/evidencepackage"
)

func packageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package <campaign-id>",
		Short: "write an archive of campaign records",
		Args:  cobra.ExactArgs(1),
		RunE:  doPackage,
	}
	cmd.Flags().String("preset", evidencepackage.Engineering, "which files and fields to include")
	cmd.Flags().String("out", "", "archive path; defaults beneath the runs directory")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doPackage(cmd *cobra.Command, args []string) error {
	result, err := evidencepackage.Build(evidencepackage.Options{
		CampaignDir: campaignRecordDir(cmd, args[0]), RunsDir: flagString(cmd, "runs-dir"),
		Output: flagString(cmd, "out"), Preset: flagString(cmd, "preset"),
	})
	if err != nil {
		return err
	}
	if strings.EqualFold(flagString(cmd, "output"), "json") {
		return writeJSON(cmd, result)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", result.Path)
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d files; %s\n", result.Files, result.SHA256)
	return nil
}
