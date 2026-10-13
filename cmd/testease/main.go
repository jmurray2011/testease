// Command testease runs scenarios and renders their records.
//
// A human at a terminal and a CI job get the same surface: the same commands,
// the same exit codes, no prompts, and no assumptions about a tty. There is no
// CI mode, because the exit codes are the integration.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
)

// version is stamped at build time with -ldflags. It goes into every record,
// so a finding can name the binary that took the measurement.
var (
	version = "dev"
	commit  = "unknown"
)

// Exit codes. The runner's status axis, not a verdict: nothing here says
// whether the news was good, only how much of it was measured.
const (
	exitOK           = 0
	exitUsage        = 2
	exitPrecondition = 3
	exitIntegrity    = 4
	exitInfraFail    = 10
	exitProbeFail    = 11
	exitInterrupted  = 12
	exitVerdict      = 20
	exitUnjudgeable  = 21
)

func main() {
	os.Exit(run())
}

func run() int {
	root := newRoot()

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "testease: "+err.Error())
		return codeFor(err)
	}
	return exitOK
}

// newRoot builds the command tree, separately from running it so a test can
// check remedies against the commands that exist.
func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "testease",
		Short:         "run scenarios and save what happened",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return validateOutputFormat(cmd)
		},

		// THERE IS NO CONFIG FILE, deliberately. Flags are the interface and
		// the environment sets their defaults, so the one thing help owes a
		// reader is which variables exist - they are otherwise discoverable
		// only by reading each command's flag defaults one at a time.
		Long: "Run scenarios, save the results, and evaluate them later.\n\n" +
			"Quick start:\n" +
			"  From a checkout with examples:\n" +
			"  make build\n" +
			"  ./testease doctor hello --scenario-root examples --strict\n" +
			"  ./testease run hello --scenario-root examples\n" +
			"  ./testease judge <run-directory>\n\n" +
			"Run measures; judge evaluates the saved record. There is no config file:\n" +
			"precedence is flag, then TESTEASE_*,\n" +
			"then the built-in default.\n\n" + envReference(),
	}
	root.PersistentFlags().String("runs-dir", envOr("TESTEASE_RUNS_DIR", defaultRunsDir()),
		"where run records are written")
	root.PersistentFlags().String("scenario-root", envOr("TESTEASE_SCENARIO_ROOT", "scenarios"),
		"directory holding scenario directories")
	root.PersistentFlags().String("log-level", envOr("TESTEASE_LOG_LEVEL", "warn"),
		"debug, info, warn or error; diagnostics go to stderr")
	root.PersistentFlags().String("log-format", envOr("TESTEASE_LOG_FORMAT", "text"),
		"text or json")

	root.AddGroup(
		&cobra.Group{ID: "core", Title: "Common commands:"},
		&cobra.Group{ID: "advanced", Title: "Other commands:"},
	)

	coreCommands := []*cobra.Command{
		validateCmd(), planCmd(), doctorCmd(), runCmd(), judgeCmd(), renderCmd(), runsCmd(), scenariosCmd(), backendsCmd(),
	}
	advancedCommands := []*cobra.Command{
		reproduceCmd(), campaignCmd(), followCmd(), packageCmd(), publishCmd(), reapCmd(), importCmd(), targetsCmd(),
	}
	for _, cmd := range coreCommands {
		cmd.GroupID = "core"
	}
	for _, cmd := range advancedCommands {
		cmd.GroupID = "advanced"
	}
	root.AddCommand(append(coreCommands, advancedCommands...)...)

	return root
}

func validateOutputFormat(cmd *cobra.Command) error {
	flag := cmd.Flags().Lookup("output")
	if flag == nil {
		return nil
	}
	value, err := cmd.Flags().GetString("output")
	if err != nil {
		return err
	}
	allowed := "text or json"
	if cmd.Name() == "render" {
		allowed = "text, json or html"
	}
	valid := value == "text" || value == "json" || cmd.Name() == "render" && value == "html"
	if !valid {
		return fmt.Errorf("--output must be one of %s, got %q", allowed, value)
	}
	return nil
}

// codeFor maps a failure to the code a caller acts on. A precondition failure
// is distinct from a run that measured badly, because they are fixed by
// different people.
func codeFor(err error) int {
	if err == nil {
		return exitOK
	}
	if code, ok := codeFromCampaign(err); ok {
		return code
	}

	var precondition *runner.PreconditionError
	if errors.As(err, &precondition) {
		return exitPrecondition
	}
	var locked *record.LockedError
	if errors.As(err, &locked) {
		return exitIntegrity
	}
	var disagrees *judge.DisagreesError
	if errors.As(err, &disagrees) {
		return exitIntegrity
	}

	var verdict *verdictError
	if errors.As(err, &verdict) {
		// A verdict nobody could reach is not a verdict against the product.
		// It maps to "could not judge", so a caller can tell "this broke" from
		// "the instruments were not there".
		if verdict.verdict == judge.Unverified {
			return exitUnjudgeable
		}
		return exitVerdict
	}
	var nothing *judge.NothingToJudgeError
	if errors.As(err, &nothing) {
		return exitUnjudgeable
	}

	var corpus *corpusError
	if errors.As(err, &corpus) {
		return exitIntegrity
	}

	var outcome *outcomeError
	if errors.As(err, &outcome) {
		// One ordering, applied once. The status the record closed with is
		// already the worst thing that happened, by record.Worse, so reading
		// the cell counts again here would be a second opinion on a question
		// that has one answer.
		switch outcome.status {
		case record.StatusProbeFail:
			return exitProbeFail
		case record.StatusInterrupted:
			return exitInterrupted
		default:
			return exitInfraFail
		}
	}
	return exitUsage
}

// outcomeError carries a run that completed but did not measure everything.
// It is an error because a caller must be able to act on it, and it is not a
// verdict because the runner does not have one.
type outcomeError struct {
	status record.Status
	runID  string
	cells  map[record.Status]int
	cause  error
	remedy string
}

func (e *outcomeError) Error() string {
	message := ""
	if e.cause != nil {
		message = fmt.Sprintf("run %s finished %s: %v", e.runID, e.status, e.cause)
	} else {
		message = fmt.Sprintf("run %s finished %s", e.runID, e.status)
	}
	if e.remedy != "" {
		message += "\n  try: " + e.remedy
	}
	return message
}

func (e *outcomeError) Unwrap() error { return e.cause }

func runError(outcome *runner.Outcome, cause error) error {
	return runErrorWithRemedy(outcome, cause, "")
}

func runErrorWithRemedy(outcome *runner.Outcome, cause error, remedy string) error {
	if cause != nil {
		if outcome != nil && outcome.Status != record.StatusMeasured {
			return &outcomeError{
				status: outcome.Status,
				runID:  outcome.RunID,
				cells:  outcome.Cells,
				cause:  cause,
				remedy: remedy,
			}
		}
		return cause
	}
	if outcome != nil && outcome.Status != record.StatusMeasured {
		return &outcomeError{status: outcome.Status, runID: outcome.RunID, cells: outcome.Cells, remedy: remedy}
	}
	return nil
}

// verdictError carries a verdict outside its judge's pass set. It is an error
// because a caller has to be able to act on it, and it is emphatically not a
// failure of the run: the run measured exactly what it set out to.
type verdictError struct {
	verdict string
	judge   string
	version string
}

func (e *verdictError) Error() string {
	return fmt.Sprintf("%s@%s: %s", e.judge, e.version, e.verdict)
}

func sortStrings(s []string) { sort.Strings(s) }

// withSignals cancels the command's context on INT or TERM so the runner can
// stop scheduling, let what is in flight finish, and close its record saying
// it was interrupted. A record that never says it was cut short reads exactly
// like a complete one.
func withSignals(cmd *cobra.Command) (context.CancelFunc, error) {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	cmd.SetContext(ctx)
	return stop, nil
}

func defaultRunsDir() string {
	if ws := os.Getenv("WORKSPACE"); ws != "" {
		return filepath.Join(ws, "scenario-runs")
	}
	return "scenario-runs"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func flagString(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return ""
	}
	return v
}

// environmentVars is every TESTEASE_* variable, and whether it is safe to
// leave set.
//
// One table, so that help and the README cannot drift apart on what exists.
// The split is provenance, not taste: a variable that changes WHERE things are
// can sit in a profile forever, and one that changes WHAT IS MEASURED cannot,
// because the record does not pin it and nothing afterwards can tell it was
// there.
func environmentVars() []struct {
	Name, Sets string
	Safe       bool
} {
	return []struct {
		Name, Sets string
		Safe       bool
	}{
		{"TESTEASE_SCENARIO_ROOT", "where scenarios are", true},
		{"TESTEASE_RUNS_DIR", "where records are written", true},
		{"TESTEASE_BASELINES", "the baseline judge's store", true},
		{"TESTEASE_FINDINGS", "findings used by publish", true},
		{"TESTEASE_RUNS_REPO", "repository used by publish", true},
		{"TESTEASE_STATEFILE_DIR", "where a provisioner wrote its state", true},
		{"TESTEASE_TARGET_CATALOG", "where target names and tags are defined", true},
		{"TESTEASE_CAMPAIGN_ROOT", "where campaign definitions are", true},
		{"TESTEASE_INPUT_BUNDLE", "where named, content-addressed inputs are described", true},
		{"TESTEASE_RUNS_URL", "where published records are readable", true},
		{"TESTEASE_AWS_PROFILE", "AWS profile for Systems Manager hosts", true},
		{"TESTEASE_AWS_REGION", "AWS region for Systems Manager hosts", true},
		{"TESTEASE_SSM_TRANSFER_STORE", "temporary store for large SSM media", true},
		{"TESTEASE_WINRM_USER", "user for hosts dialled over WinRM", true},
		{"TESTEASE_WINRM_PASSWORD_FILE", "password file for WinRM hosts", true},
		{"TESTEASE_SSH_USER", "user for hosts dialled over SSH", true},
		{"TESTEASE_SSH_PASSWORD_FILE", "password file for SSH hosts", true},
		{"TESTEASE_SSH_KEY", "private key for SSH hosts", true},
		{"TESTEASE_SSH_KNOWN_HOSTS", "known_hosts file for SSH hosts", true},
		{"TESTEASE_LOG_LEVEL", "debug, info, warn or error", true},
		{"TESTEASE_LOG_FORMAT", "text or json", true},
		{"TESTEASE_STORE", "the artifact store an axis discovers from", false},
		{"TESTEASE_DOCKER_CONTEXT", "which daemon holds the box", false},
		{"TESTEASE_WIN_DOCKER_CONTEXT", "which daemon holds one-shot Windows cells", false},
		{"TESTEASE_INSTRUMENT_NAME", "what to record the instrument as", false},
		{"TESTEASE_INSTRUMENT_COLLECT", "the fingerprint instrument", false},
		{"TESTEASE_INSTRUMENT_DIFF", "how two of its readings are compared", false},
		{"TESTEASE_WIN_MEMORY", "what a one-shot Windows cell gets", false},
		{"TESTEASE_ENV_FILE", "secrets injected into drive steps", false},
		{"TESTEASE_COMPARE", "baseline comparison command", false},
	}
}

// envReference is the environment section of the root help.
func envReference() string {
	var b strings.Builder
	b.WriteString("Environment variables for operator defaults and connection setup:\n")
	for _, v := range environmentVars() {
		if v.Safe {
			fmt.Fprintf(&b, "  %-28s %s\n", v.Name, v.Sets)
		}
	}
	b.WriteString("\nPass these as flags - they change what is measured, recorded, or judged:\n")
	for _, v := range environmentVars() {
		if !v.Safe {
			fmt.Fprintf(&b, "  %-28s %s\n", v.Name, v.Sets)
		}
	}
	b.WriteString("\n--param, --matrix, --resume, --keep, --par and --promote must be passed as\nflags because they change the run or what a rerun means.")
	return b.String()
}
