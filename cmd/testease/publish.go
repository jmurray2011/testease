package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/publish"
)

func publishCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "copy cited run records to a shared directory",
		Long: "This copies only the run records named by findings. It does not copy every\n" +
			"record in the runs directory. Findings use run IDs; URLs are added only when\n" +
			"you provide --runs-url.",
		Args: cobra.NoArgs,
		RunE: doPublish,
	}
	cmd.Flags().String("findings", envOr("TESTEASE_FINDINGS", "findings"), "directory containing finding files")
	cmd.Flags().String("runs-repo", envOr("TESTEASE_RUNS_REPO", ""), "repository published records are copied into")
	cmd.Flags().String("runs-url", os.Getenv("TESTEASE_RUNS_URL"), "where a published record is readable, for printing how a citation resolves")
	cmd.Flags().StringArray("runs-root", nil, "extra directory to look for records in (repeatable)")
	cmd.Flags().Bool("stage", false, "copy the cited records into the runs repository")
	cmd.Flags().Bool("commit", false, "commit the records copied by --stage")
	cmd.Flags().Bool("push", false, "push the commit made by --commit")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

// publishReport is how every citation in the corpus resolved. Missing ones are
// listed rather than skipped: a citation that resolves to nothing is a claim of
// evidence nobody can reach.
type publishReport struct {
	Findings string           `json:"findings"`
	Cited    int              `json:"cited"`
	Resolved []publishRun     `json:"resolved"`
	Missing  []publishMissing `json:"missing"`
	Defects  []publishDefect  `json:"defects"`
	Staged   []string         `json:"staged"`
}

type publishRun struct {
	RunID string `json:"run_id"`
	Dir   string `json:"dir"`
	URL   string `json:"url,omitempty"`
}

type publishMissing struct {
	RunID   string   `json:"run_id"`
	Sources []string `json:"cited_by"`
}

type publishDefect struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	Why    string `json:"why"`
}

func doPublish(cmd *cobra.Command, _ []string) error {
	roots, err := cmd.Flags().GetStringArray("runs-root")
	if err != nil {
		return err
	}
	roots = append(roots, flagString(cmd, "runs-dir"))

	plan, err := publish.Prepare(flagString(cmd, "findings"), roots)
	if err != nil {
		return err
	}

	url := strings.TrimRight(flagString(cmd, "runs-url"), "/")
	report := publishReport{
		Findings: flagString(cmd, "findings"),
		Cited:    len(plan.Resolved) + len(plan.Missing),
		Resolved: []publishRun{}, Missing: []publishMissing{},
		Defects: []publishDefect{}, Staged: []string{},
	}
	for _, r := range plan.Resolved {
		run := publishRun{RunID: r.RunID, Dir: r.Dir}
		if url != "" {
			run.URL = url + "/" + r.RunID
		}
		report.Resolved = append(report.Resolved, run)
	}
	for _, m := range plan.Missing {
		report.Missing = append(report.Missing, publishMissing{RunID: m.RunID, Sources: m.Sources})
	}
	for _, d := range plan.Defects {
		report.Defects = append(report.Defects, publishDefect{Text: d.Text, Source: d.Source, Why: d.Why})
	}

	asJSON := flagString(cmd, "output") == "json"
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if !asJSON {
		writePublish(cmd, report)
	}

	stage, _ := cmd.Flags().GetBool("stage")
	commit, _ := cmd.Flags().GetBool("commit")
	push, _ := cmd.Flags().GetBool("push")
	if commit && !stage {
		return fmt.Errorf("--commit requires --stage: committing must not imply copying files")
	}
	if push && !commit {
		return fmt.Errorf("--push requires --commit: pushing must name the commit this invocation made")
	}
	if !stage {
		if asJSON {
			if err := writeJSON(cmd, report); err != nil {
				return err
			}
		} else {
			_, _ = fmt.Fprintf(errOut, "nothing copied; --stage to write them into the runs repository\n")
		}
		return incomplete(plan)
	}

	repo := flagString(cmd, "runs-repo")
	if repo == "" {
		return fmt.Errorf("no runs repository given; --runs-repo <dir> or TESTEASE_RUNS_REPO")
	}
	staged, err := publish.Stage(repo, plan)
	if err != nil {
		return err
	}
	report.Staged = append(report.Staged, staged...)

	if err := recordPublication(cmd, repo, staged, commit, push); err != nil {
		return err
	}

	if asJSON {
		if err := writeJSON(cmd, report); err != nil {
			return err
		}
		return incomplete(plan)
	}

	for _, id := range staged {
		_, _ = fmt.Fprintf(out, "staged   %s\n", id)
	}
	if len(staged) == 0 {
		_, _ = fmt.Fprintln(errOut, "nothing new to publish")
	}
	if url != "" {
		_, _ = fmt.Fprintln(errOut, "\nhow a citation resolves:")
		for _, r := range report.Resolved {
			_, _ = fmt.Fprintf(errOut, "  %s\n      %s\n", r.RunID, r.URL)
		}
	}
	return incomplete(plan)
}

func writePublish(cmd *cobra.Command, report publishReport) {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	_, _ = fmt.Fprintf(errOut, "%d run id(s) cited by %s\n", report.Cited, report.Findings)
	for _, r := range report.Resolved {
		_, _ = fmt.Fprintf(out, "ok       %s\t%s\n", r.RunID, r.Dir)
	}
	for _, m := range report.Missing {
		_, _ = fmt.Fprintf(out, "MISSING  %s\tcited by %s\n", m.RunID, strings.Join(m.Sources, ", "))
	}
	for _, d := range report.Defects {
		_, _ = fmt.Fprintf(errOut, "defect   %s in %s\n  %s\n", d.Text, d.Source, d.Why)
	}
}

// recordPublication records the newly published records, if the repository is one.
func recordPublication(cmd *cobra.Command, repo string, staged []string, commit, push bool) error {
	if !commit {
		return nil
	}
	if len(staged) == 0 {
		return fmt.Errorf("--commit was requested, but no new records were copied")
	}
	if err := exec.Command("git", "-C", repo, "rev-parse", "--git-dir").Run(); err != nil { //nolint:gosec // a path the caller gave
		return fmt.Errorf("--commit requires the runs repository to be a git repository: %s", repo)
	}

	// A publication commit must contain exactly this invocation's records. An
	// existing staged change belongs to its author, and folding it into an
	// automatic commit would turn --commit into "commit whatever is nearby".
	if clean, err := gitQuiet(repo, "diff", "--cached", "--quiet"); err != nil {
		return err
	} else if !clean {
		return fmt.Errorf("the runs repository already has staged changes; commit or unstage them before testease publish --commit")
	}

	if push {
		// git push sends every local commit the branch is ahead by, not merely
		// the one this command is about. Refuse before making the publication
		// commit when an earlier local commit would hitch a ride.
		out, err := gitOutput(repo, "rev-list", "--count", "@{upstream}..HEAD")
		if err != nil {
			return fmt.Errorf("--push needs a configured upstream so testease can prove no other commits will be pushed: %w", err)
		}
		if strings.TrimSpace(out) != "0" {
			return fmt.Errorf("the runs repository is already ahead of its upstream; refusing to push unrelated commits")
		}
	}

	args := append([]string{"add", "--"}, staged...)
	if err := git(repo, args...); err != nil {
		return err
	}
	message := fmt.Sprintf("publish %d run record(s) cited by findings", len(staged))
	if err := git(repo, "commit", "-q", "-m", message); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "committed")
	if push {
		if err := git(repo, "push"); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "pushed")
	}
	return nil
}

// gitQuiet runs a git predicate: exit 0 is true, exit 1 is false, and every
// other result is an operational error worth reporting rather than guessing.
func gitQuiet(repo string, args ...string) (bool, error) {
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput() //nolint:gosec // a path the caller gave
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
}

func gitOutput(repo string, args ...string) (string, error) {
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput() //nolint:gosec // a path the caller gave
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func git(repo string, args ...string) error {
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput() //nolint:gosec // a path the caller gave
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// incomplete turns a corpus that cites what nobody can reach into an exit code,
// because that is a defect in the corpus rather than a failure to publish.
func incomplete(plan publish.Plan) error {
	if plan.Complete() {
		return nil
	}
	return &corpusError{missing: len(plan.Missing), defects: len(plan.Defects)}
}

type corpusError struct{ missing, defects int }

func (e *corpusError) Error() string {
	return fmt.Sprintf("the corpus cites %d record(s) nobody can reach and has %d defect(s)", e.missing, e.defects)
}
