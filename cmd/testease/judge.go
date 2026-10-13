package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

func judgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "judge <run-id>",
		Short: "read a record and reach a verdict, without re-measuring anything",
		Long: "This reads a saved record and does not run the scenario. You can judge an\n" +
			"old record again with a different judge or a newer judge version.",
		Args: cobra.ExactArgs(1),
		RunE: doJudge,
	}
	cmd.Flags().String("judge", "expect", "which judge to read the record with, name or name@version")
	cmd.Flags().String("judge-command", "", "external judge command; it receives the record directory and writes a JSON result")
	cmd.Flags().String("judge-name", "", "name to record for an external judge")
	cmd.Flags().String("judge-version", "1", "version to record for an external judge")
	cmd.Flags().StringArray("pass-verdict", []string{"pass"}, "verdict an external judge treats as passing (repeatable)")
	cmd.Flags().String("baselines", envOr("TESTEASE_BASELINES", "baselines"), "directory the baseline judge keeps baselines in")
	cmd.Flags().String("compare", os.Getenv("TESTEASE_COMPARE"), "command the baseline judge compares with: <cmd> <baseline> <report>, exit 0 for no regressions")
	cmd.Flags().Bool("force-baseline", false, "advance the baseline even when the verdict is hold; the verdict still says hold")
	cmd.Flags().Bool("promote", false, "advance the baseline when the verdict says to; judging alone never moves it")
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doJudge(cmd *cobra.Command, args []string) error {
	if err := checkPromotionFlags(cmd); err != nil {
		return err
	}

	dir := recordDir(cmd, args[0])
	rec, err := record.Read(dir)
	if err != nil {
		return err
	}
	// A census's evidence is in its cells, and a verdict reached over a cell
	// the reader had to repair rests on that repair. Said before judging
	// rather than beside the verdict, so a record the judge refuses to read
	// still reports what was wrong with it.
	if err := reportRepairs(cmd, rec); err != nil {
		return err
	}

	// A record says how it was meant to be read. An explicit --judge overrides
	// that, which is the whole point of re-judging: reading old evidence with
	// a judge the scenario never named.
	requested := flagString(cmd, "judge")
	name, version, _ := strings.Cut(requested, "@")
	var external bool
	var j judge.Judge
	if cmd.Flags().Changed("judge-command") {
		external = true
		if cmd.Flags().Changed("judge") {
			return fmt.Errorf("--judge and --judge-command cannot be used together")
		}
		name := flagString(cmd, "judge-name")
		version := flagString(cmd, "judge-version")
		if name == "" {
			return fmt.Errorf("--judge-command needs --judge-name")
		}
		if !validJudgePart(name) || !validJudgePart(version) {
			return fmt.Errorf("--judge-name and --judge-version may contain only letters, numbers, '.', '_' and '-'")
		}
		command, err := parseJudgeCommand(flagString(cmd, "judge-command"))
		if err != nil {
			return err
		}
		j = externalJudge{Context: cmd.Context(), NameValue: name, VersionValue: version, Command: command}
	} else {
		if !cmd.Flags().Changed("judge") {
			if declared := declaredJudge(rec); declared != "" {
				requested = declared
				name, version, _ = strings.Cut(requested, "@")
			}
		}
		j, err = judge.ByName(name)
		if err != nil {
			return err
		}
		if _, isBaseline := j.(judge.Baseline); isBaseline {
			j = judge.Baseline{
				Store:   judge.DirStore{Dir: flagString(cmd, "baselines")},
				Compare: strings.Fields(flagString(cmd, "compare")),
			}
		}
		if forced, _ := cmd.Flags().GetBool("force-baseline"); forced {
			if _, isBaseline := j.(judge.Baseline); !isBaseline {
				return fmt.Errorf("--force-baseline qualifies the baseline judge, and %s keeps no baseline", j.Name())
			}
		}
		if _, isCompare := j.(judge.Compare); isCompare {
			// The subjects come off the record rather than off the scenario tree,
			// so re-judging a two-year-old run needs the run and nothing else -
			// which is the promise that makes interpretation replayable.
			params := declaredParams(rec)
			j = judge.Compare{Same: params["same"], Differ: params["differ"]}
		}
	}
	if external {
		name = j.Name()
		version = ""
	}
	// An older judge version cannot be recomputed by a binary that no longer
	// contains it - that is what versioning a judge is for. What it can do is
	// show what that version concluded, from the file it wrote into the
	// record, which is what a finding citing (run-id, judge@version) points at.
	if version != "" && version != j.Version() {
		stored, err := judge.Load(dir, name, version)
		if err != nil {
			return fmt.Errorf("this testease ships %s@%s and cannot recompute @%s, and the record holds no verdict from it: %w",
				j.Name(), j.Version(), version, err)
		}
		if err := reportVerdict(cmd, stored, "", rec); err != nil {
			return err
		}
		// The same exit code whether a verdict was reached now or read from
		// the record. A caller acting on it cannot tell the difference and
		// must not have to. The pass set is the shipped judge's, because the
		// implementation that wrote this file is gone - which is worth knowing
		// if a judge ever changes its vocabulary rather than its conclusions.
		if !passedFor(cmd, stored.Verdict, external) {
			return &verdictError{verdict: stored.Verdict, judge: stored.Judge, version: stored.Version}
		}
		return nil
	}

	verdict, err := judge.Apply(j, rec)
	if err != nil {
		return err
	}
	outcome, err := judge.Save(dir, verdict)
	if err != nil {
		return err
	}

	if err := reportVerdict(cmd, verdict, outcome.String(), rec); err != nil {
		return err
	}
	if err := promote(cmd, j, verdict, rec); err != nil {
		return err
	}
	if !passedFor(cmd, verdict.Verdict, external) {
		return &verdictError{verdict: verdict.Verdict, judge: j.Name(), version: j.Version()}
	}
	return nil
}

func parseJudgeCommand(raw string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	started := false
	for _, r := range raw {
		if escaped {
			current.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				started = true
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			current.WriteRune(r)
			started = true
			continue
		}
		if r == '\\' {
			escaped = true
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
			continue
		}
		current.WriteRune(r)
		started = true
	}
	if escaped {
		return nil, fmt.Errorf("--judge-command ends with an unfinished escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("--judge-command has an unterminated quote")
	}
	if started {
		args = append(args, current.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("--judge-command cannot be empty")
	}
	return args, nil
}

func validJudgePart(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func passedFor(cmd *cobra.Command, verdict string, external bool) bool {
	if !external {
		return passed(verdict)
	}
	allowed, err := cmd.Flags().GetStringArray("pass-verdict")
	if err != nil {
		return false
	}
	return slices.Contains(allowed, verdict)
}

// checkPromotionFlags refuses a flag combination that cannot mean anything,
// before the record is opened.
//
// Checked after judging, a misuse surfaces as the judge's own complaint about
// the evidence - "this record declares no expectations" is true, and is not
// what the operator got wrong.
func checkPromotionFlags(cmd *cobra.Command) error {
	forced, _ := cmd.Flags().GetBool("force-baseline")
	asked, _ := cmd.Flags().GetBool("promote")
	if forced && !asked {
		// Silently ignoring the flag would let someone believe they accepted
		// a hold when nothing moved.
		return fmt.Errorf("--force-baseline does nothing on its own: it qualifies --promote on the baseline judge")
	}
	return nil
}

// reportRepairs says what the reader had to tolerate to read this record and
// the cells underneath it. Repairs are read-time and reported; a projection
// that stays silent about one is the record lying by omission.
func reportRepairs(cmd *cobra.Command, rec *record.Record) error {
	for _, reports := range [][]record.Report{rec.Reports, record.CellReports(rec)} {
		for _, report := range reports {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s\n", report.About, report.Message); err != nil {
				return fmt.Errorf("report repairs: %w", err)
			}
		}
	}
	return nil
}

// reportVerdict prints a verdict. An empty outcome means it was read from the
// record rather than reached now, so there is nothing to say about writing.
func reportVerdict(cmd *cobra.Command, v judge.Verdict, outcome string, rec *record.Record) error {
	// A record that never closed is a status, not a verdict, and a reader
	// weighing the verdict below has to know the run was cut short.
	if rec.Interrupted() {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
			"warning: this run was interrupted - the verdict covers only what was measured before it stopped")
	}

	if strings.EqualFold(flagString(cmd, "output"), "json") {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s@%s\t%s\n", v.Judge, v.Version, v.Verdict)
	// Steps for a single target, cells for a census. A judge fills whichever
	// its unit is, and a reader takes whichever is there.
	for _, subjects := range []map[string]judge.StepVerdict{v.Steps, v.Cells} {
		for _, label := range slices.Sorted(maps.Keys(subjects)) {
			subject := subjects[label]
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s\n", subject.Verdict, label)
			for _, because := range subject.Because {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "               %s\n", because)
			}
		}
	}
	for _, label := range slices.Sorted(maps.Keys(v.Unjudged)) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s (%s)\n", "unjudged", label, v.Unjudged[label])
	}
	if outcome != "" {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "verdict %s: verdicts/%s@%s.json\n", outcome, v.Judge, v.Version)
	}
	return nil
}

// recordDir accepts either a path to a record or a run id under the runs
// directory, because both are what a person has to hand.
func recordDir(cmd *cobra.Command, arg string) string {
	if _, err := os.Stat(filepath.Join(arg, record.EventsFile)); err == nil {
		return arg
	}
	return filepath.Join(flagString(cmd, "runs-dir"), arg)
}

// declaredParams is what the scenario told its judge about its own question,
// read off the record. Nil when the scenario said nothing, which for a judge
// that needs subjects is a refusal rather than a default.
func declaredParams(rec *record.Record) map[string][]string {
	for _, e := range rec.Events {
		if e.Kind != record.KindRunStarted {
			continue
		}
		started, err := record.Decode[record.RunStarted](e)
		if err != nil || started.Judge == nil {
			return nil
		}
		return started.Judge.Params
	}
	return nil
}

// declaredJudge is the judge the scenario named, read off the record. Empty
// when the scenario named none - in which case the flag's own default stands,
// and nothing is inferred from the shape of the evidence.
func declaredJudge(rec *record.Record) string {
	for _, e := range rec.Events {
		if e.Kind != record.KindRunStarted {
			continue
		}
		started, err := record.Decode[record.RunStarted](e)
		if err != nil || started.Judge == nil {
			return ""
		}
		if started.Judge.Version == "" {
			return started.Judge.Name
		}
		return started.Judge.Name + "@" + started.Judge.Version
	}
	return ""
}

// passed is whether a verdict is in its judge's pass set.
func passed(verdict string) bool {
	switch verdict {
	case judge.Pass, judge.Promote, judge.Established, judge.Restored, judge.ReleaseReady:
		return true
	default:
		return false
	}
}

// promote advances the baseline, when asked and when the verdict says to.
//
// Judging never moves a baseline on its own. A judge that promoted as it
// judged would stop being a function of the record - re-judging the same run
// would compare its report against itself and reach a different answer - so
// moving it is a separate act, taken by whoever asked for the judging.
func promote(cmd *cobra.Command, j judge.Judge, verdict judge.Verdict, rec *record.Record) error {
	asked, _ := cmd.Flags().GetBool("promote")
	forced, _ := cmd.Flags().GetBool("force-baseline")
	// The flags were checked against each other and against the judge before
	// any evidence was read; by here they can only be a combination that means
	// something.
	baseline, ok := j.(judge.Baseline)
	if !ok || !asked {
		return nil
	}
	switch verdict.Verdict {
	case judge.Promote, judge.Established:
	default:
		if !forced {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"baseline not advanced: the verdict is %s\n", verdict.Verdict)
			return nil
		}
		// Someone decided to accept this anyway. The verdict still says what
		// the comparison found; overriding it is an act, and an act says so.
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"accepting a %s as the new baseline because --force-baseline was given\n", verdict.Verdict)
	}

	report, ok := reportPath(rec)
	if !ok {
		return fmt.Errorf("this record names no report to promote")
	}
	body, err := os.Open(filepath.Join(rec.Dir, filepath.FromSlash(report)))
	if err != nil {
		return fmt.Errorf("read the report: %w", err)
	}
	defer func() { _ = body.Close() }()

	key := baseline.Key(rec)
	if err := baseline.Store.Promote(key, body); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "baseline %s advanced from %s\n", key, report)
	return nil
}

// reportPath is where this run said its report ended up. Later readings win.
func reportPath(rec *record.Record) (string, bool) {
	path := ""
	found := false
	for _, e := range rec.Latest().Events {
		if e.Kind != record.KindFact {
			continue
		}
		fact, err := record.Decode[record.Fact](e)
		if err != nil || fact.Key != record.ReportFact || fact.Value == nil {
			continue
		}
		path, found = *fact.Value, true
	}
	return path, found
}
