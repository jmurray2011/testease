package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/reap"
)

func targetsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "targets",
		Short: "the targets standing right now, and how to get onto them",
		Long: "A run that did not finish cleanly leaves its targets up, because a half-mutated\n" +
			"box answers questions the record does not. This says what is there and prints\n" +
			"the command that opens a shell on each.\n\n" +
			"Whether one is safe to remove is a different question, and testease reap has it.",
		Args: cobra.NoArgs,
		RunE: doTargets,
	}
	cmd.Flags().StringArray("docker-contexts", nil, "docker contexts to look in (repeatable); the current one by default")
	cmd.Flags().Bool("all-contexts", false, "look in every docker context configured here")
	cmd.Flags().String("run", "", "only the targets of this run")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	cmd.AddCommand(targetCatalogCmd())
	return cmd
}

type standingTarget struct {
	Name    string `json:"name"`
	Where   string `json:"where"`
	RunID   string `json:"run,omitempty"`
	Host    string `json:"host,omitempty"`
	Cell    string `json:"cell,omitempty"`
	Backend string `json:"backend,omitempty"`
	Created string `json:"created,omitempty"`
	Fate    string `json:"fate"`
	Shell   string `json:"shell"`
}

func doTargets(cmd *cobra.Command, _ []string) error {
	log, err := logger(cmd)
	if err != nil {
		return err
	}

	contexts, notices := dockerContexts(cmd, log)

	var found []reap.Target
	for _, dockerContext := range contexts {
		client := backend.Client{Context: dockerContext, Log: log}
		labelled, err := client.Labelled(cmd.Context(), reap.LabelRunner)
		if err != nil {
			// A daemon that did not answer is not an empty daemon.
			notices = append(notices, fmt.Sprintf("skipped %s: %v", describeContext(dockerContext), err))
			continue
		}
		for name, labels := range labelled {
			found = append(found, reap.Target{
				Name: name, Where: describeContext(dockerContext), Labels: labels,
			})
		}
	}

	host, _ := os.Hostname()
	self := reap.Runner{User: os.Getenv("USER"), Host: host, AllUsers: true}
	wanted := flagString(cmd, "run")

	standing := []standingTarget{}
	for _, t := range reap.Assess(found, self, reap.Running) {
		if wanted != "" && t.RunID() != wanted {
			continue
		}
		standing = append(standing, standingTarget{
			Name: t.Name, Where: t.Where, RunID: t.RunID(),
			Host:    t.Labels[reap.LabelHost],
			Cell:    t.Labels[reap.LabelCell],
			Backend: t.Labels[reap.LabelBackend],
			Created: t.Created(),
			Fate:    string(t.Fate),
			Shell:   shellFor(t),
		})
	}
	sort.Slice(standing, func(i, j int) bool { return standing[i].Name < standing[j].Name })

	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, struct {
			Targets []standingTarget `json:"targets"`
			Notices []string         `json:"notices"`
		}{Targets: standing, Notices: notices})
	}

	errOut := cmd.ErrOrStderr()
	for _, n := range notices {
		_, _ = fmt.Fprintln(errOut, n)
	}
	if len(standing) == 0 {
		_, _ = fmt.Fprintln(errOut, "no targets standing")
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "name\thost\trun\tfate\tshell"); err != nil {
		return fmt.Errorf("list targets: %w", err)
	}
	for _, t := range standing {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			t.Name, or(t.Host, "-"), or(t.RunID, "-"), t.Fate, t.Shell); err != nil {
			return fmt.Errorf("list targets: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("list targets: %w", err)
	}
	return nil
}

// shellFor is the command that opens a shell on a target. Printing it is the
// point of the command: the reason a target is left standing is that somebody
// wants to look at it.
func shellFor(t reap.Target) string {
	if context := contextOf(t.Where); context != "" {
		return fmt.Sprintf("docker --context %s exec -it %s bash", context, t.Name)
	}
	return fmt.Sprintf("docker exec -it %s bash", t.Name)
}
