package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/reap"
)

func reapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reap",
		Short: "find targets left by failed runs",
		Long: "A failed run may leave targets running for inspection. This command lists\n" +
			"targets that can be removed after the process that created them has exited.\n\n" +
			"It does not use age as a removal rule.",
		Args: cobra.NoArgs,
		RunE: doReap,
	}
	cmd.Flags().Bool("force", false, "remove the orphans rather than listing them")
	cmd.Flags().Bool("all-users", false, "widen to every user on this machine; it cannot widen to other machines")
	cmd.Flags().StringArray("docker-contexts", nil, "docker contexts to look in (repeatable); the current one by default")
	cmd.Flags().Bool("all-contexts", false, "look in every docker context configured here")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

// reapReport is what reap found. Notices are in it rather than beside it,
// because a daemon that could not be reached is part of the answer: treating it
// as "no orphans there" is how a leak on the machine nobody looks at goes on
// forever.
type reapReport struct {
	Targets   []reapTarget `json:"targets"`
	Removable int          `json:"removable"`
	Removed   []string     `json:"removed"`
	Notices   []string     `json:"notices"`
}

type reapTarget struct {
	// Kind is what it is, because a network and a container are abandoned for
	// the same reason and removed by different commands.
	Kind    string `json:"kind"`
	Fate    string `json:"fate"`
	Where   string `json:"where"`
	Name    string `json:"name"`
	RunID   string `json:"run,omitempty"`
	Created string `json:"created,omitempty"`
	Reason  string `json:"reason"`
}

func doReap(cmd *cobra.Command, _ []string) error {
	log, err := logger(cmd)
	if err != nil {
		return err
	}

	contexts, contextNotices := dockerContexts(cmd, log)
	allUsers, _ := cmd.Flags().GetBool("all-users")
	host, _ := os.Hostname()
	self := reap.Runner{User: os.Getenv("USER"), Host: host, AllUsers: allUsers}

	report := reapReport{Targets: []reapTarget{}, Removed: []string{}, Notices: contextNotices}

	var targets, networks []reap.Target
	for _, dockerContext := range contexts {
		client := backend.Client{Context: dockerContext, Log: log}
		found, err := client.Labelled(cmd.Context(), reap.LabelRunner)
		if err != nil {
			report.Notices = append(report.Notices,
				fmt.Sprintf("skipped %s: %v", describeContext(dockerContext), err))
			continue
		}
		for name, labels := range found {
			targets = append(targets, reap.Target{
				Name: name, Where: describeContext(dockerContext), Labels: labels,
			})
		}

		// Networks outlive the containers on them: a target removed by hand
		// leaves its network behind, and nothing else would ever come back for
		// it. Docker refuses to remove one with containers still attached, so a
		// live cell's network is safe from this by the daemon rather than by a
		// count kept here.
		nets, err := client.LabelledNetworks(cmd.Context(), reap.LabelRunner)
		if err != nil {
			report.Notices = append(report.Notices,
				fmt.Sprintf("could not list networks on %s: %v", describeContext(dockerContext), err))
			continue
		}
		for name, labels := range nets {
			networks = append(networks, reap.Target{
				Name: name, Where: describeContext(dockerContext), Labels: labels,
			})
		}
	}

	assessed := reap.Assess(targets, self, reap.Running)
	assessedNets := reap.Assess(networks, self, reap.Running)
	runtime, runtimeNotices := assessRuntimeWorkspaces(flagString(cmd, "runs-dir"), self)
	report.Notices = append(report.Notices, runtimeNotices...)
	for _, t := range assessed {
		report.Targets = append(report.Targets, reapTarget{
			Kind: "container", Fate: string(t.Fate), Where: t.Where, Name: t.Name,
			RunID: t.RunID(), Created: t.Created(), Reason: t.Reason,
		})
	}
	for _, t := range assessedNets {
		report.Targets = append(report.Targets, reapTarget{
			Kind: "network", Fate: string(t.Fate), Where: t.Where, Name: t.Name,
			RunID: t.RunID(), Created: t.Created(), Reason: t.Reason,
		})
	}
	for _, item := range runtime {
		report.Targets = append(report.Targets, reapTarget{
			Kind: "secret-files", Fate: string(item.Fate), Where: "filesystem", Name: item.Dir,
			RunID: item.Owner.RunID, Reason: item.Reason,
		})
	}
	report.Removable = len(reap.Removable(assessed)) + len(reap.Removable(assessedNets)) + len(removableRuntime(runtime))

	force, _ := cmd.Flags().GetBool("force")
	if force {
		for _, t := range reap.Removable(assessed) {
			client := backend.Client{Context: contextOf(t.Where), Log: log}
			if err := client.Remove(cmd.Context(), t.Name); err != nil {
				report.Notices = append(report.Notices,
					fmt.Sprintf("could not remove %s: %v", t.Name, err))
				continue
			}
			report.Removed = append(report.Removed, t.Name)
		}
		// Networks last: docker refuses one that still has a container on it,
		// and the containers have only just gone.
		for _, t := range reap.Removable(assessedNets) {
			client := backend.Client{Context: contextOf(t.Where), Log: log}
			if err := client.RemoveNetwork(cmd.Context(), t.Name); err != nil {
				report.Notices = append(report.Notices,
					fmt.Sprintf("could not remove network %s: %v", t.Name, err))
				continue
			}
			report.Removed = append(report.Removed, t.Name)
		}
		for _, item := range removableRuntime(runtime) {
			if err := removeRuntimeWorkspace(flagString(cmd, "runs-dir"), item.Dir); err != nil {
				report.Notices = append(report.Notices, fmt.Sprintf("could not remove secret workspace %s: %v", item.Dir, err))
				continue
			}
			report.Removed = append(report.Removed, item.Dir)
		}
	}

	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, report)
	}
	writeReap(cmd, report, force)
	return nil
}

const runtimeWorkDir = ".testease-runtime"

type runtimeWorkspace struct {
	Dir    string
	Owner  runtimeWorkspaceOwner
	Fate   reap.Fate
	Reason string
}

type runtimeWorkspaceOwner struct {
	RunID string `json:"run_id"`
	Cell  string `json:"cell,omitempty"`
	User  string `json:"user"`
	Host  string `json:"host"`
	PID   int    `json:"pid"`
}

func assessRuntimeWorkspaces(runsDir string, self reap.Runner) ([]runtimeWorkspace, []string) {
	root := filepath.Join(runsDir, runtimeWorkDir)
	var out []runtimeWorkspace
	var notices []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "owner.json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			notices = append(notices, fmt.Sprintf("could not read secret workspace owner %s: %v", path, err))
			return nil
		}
		var owner runtimeWorkspaceOwner
		if err := json.Unmarshal(body, &owner); err != nil {
			notices = append(notices, fmt.Sprintf("could not parse secret workspace owner %s: %v", path, err))
			return nil
		}
		labels := map[string]string{
			reap.LabelRunner: owner.User + "@" + owner.Host,
			reap.LabelPID:    fmt.Sprint(owner.PID),
			reap.LabelRun:    owner.RunID,
		}
		fate, reason := reap.Decide(labels, self, reap.Running)
		out = append(out, runtimeWorkspace{Dir: filepath.Dir(path), Owner: owner, Fate: fate, Reason: reason})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		notices = append(notices, fmt.Sprintf("could not inspect secret workspaces under %s: %v", root, err))
	}
	return out, notices
}

func removableRuntime(items []runtimeWorkspace) []runtimeWorkspace {
	var out []runtimeWorkspace
	for _, item := range items {
		if item.Fate == reap.Orphaned {
			out = append(out, item)
		}
	}
	return out
}

func removeRuntimeWorkspace(runsDir, workspace string) error {
	root := filepath.Clean(filepath.Join(runsDir, runtimeWorkDir))
	rel, err := filepath.Rel(root, filepath.Clean(workspace))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("runtime workspace %s is not a child of %s", workspace, root)
	}
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	for parent := filepath.Dir(filepath.Clean(workspace)); ; parent = filepath.Dir(parent) {
		if err := os.Remove(parent); err != nil {
			return nil
		}
		if parent == root {
			return nil
		}
	}
}

func writeReap(cmd *cobra.Command, report reapReport, force bool) {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	for _, n := range report.Notices {
		_, _ = fmt.Fprintln(errOut, n)
	}
	for _, t := range report.Targets {
		_, _ = fmt.Fprintf(out, "%-11s %-9s %s\t%s\trun=%s\n",
			t.Fate, t.Kind, t.Where, t.Name, or(t.RunID, "-"))
		_, _ = fmt.Fprintf(errOut, "            %s\n", t.Reason)
	}
	if !force {
		_, _ = fmt.Fprintf(errOut,
			"\n%d of %d target(s) can be proved abandoned; nothing removed. --force removes them\n",
			report.Removable, len(report.Targets))
		return
	}
	for _, name := range report.Removed {
		_, _ = fmt.Fprintf(out, "removed     %s\n", name)
	}
}

func describeContext(name string) string {
	if name == "" {
		return "docker"
	}
	return "docker:" + name
}

// contextOf runs describeContext backwards. Only the "docker:" prefix comes
// off: trimming a bare "docker" too would mangle any context whose own name
// starts with the word - "dockersrv" became "srv", and every removal dialled
// a daemon that does not exist.
func contextOf(where string) string {
	if where == "docker" {
		return ""
	}
	return strings.TrimPrefix(where, "docker:")
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// dockerContexts is where to look for standing targets. Named contexts win; the
// alternative is every context configured here, because a target on a daemon
// nobody named is a leak nobody sees.
func dockerContexts(cmd *cobra.Command, log *slog.Logger) ([]string, []string) {
	named, err := cmd.Flags().GetStringArray("docker-contexts")
	if err == nil && len(named) > 0 {
		return named, nil
	}

	if all, _ := cmd.Flags().GetBool("all-contexts"); all {
		names, err := (backend.Client{Log: log}).Contexts(cmd.Context())
		if err != nil {
			return []string{""}, []string{"could not list docker contexts: " + err.Error()}
		}
		return names, nil
	}
	return []string{""}, nil
}
