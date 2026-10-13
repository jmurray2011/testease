package judge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// Release-readiness speaks about the installed product, not whether the
// experiment managed to take all of its readings. Matrix remains the evidence
// contract; this is the release gate over the same immutable record.
const (
	ReleaseReady            = "release-ready"
	ReleaseBlocked          = "release-blocked"
	UpgradeFailed           = "upgrade-failed"
	ManagerTransitionFailed = "manager-transition-failed"
	ApplicationFailed       = "application-failed"
	PayloadDrift            = "payload-drift"
)

// ReleaseReadiness derives a product outcome for an upgrade census from the
// primitive facts recorded by upgrade-sweep.
type ReleaseReadiness struct{}

var _ Judge = ReleaseReadiness{}

func (ReleaseReadiness) Name() string    { return "release-readiness" }
func (ReleaseReadiness) Version() string { return "3" }

func (r ReleaseReadiness) Judge(rec *record.Record) (Result, error) {
	keys, statuses, err := cells(rec)
	if err != nil {
		return Result{}, err
	}
	if len(keys) == 0 {
		return Result{}, &NothingToJudgeError{Judge: r.Name()}
	}

	result := Result{
		Verdict:      ReleaseReady,
		Cells:        map[string]StepVerdict{},
		Unjudged:     map[string]string{},
		CellEvidence: map[string]string{},
	}
	for _, key := range keys {
		if status := statuses[key]; status != record.StatusMeasured {
			result.Unjudged[key] = string(status)
			continue
		}

		cell, err := record.Read(filepath.Join(rec.Dir, record.CellsDir, key))
		if err != nil {
			result.Unjudged[key] = "the cell's record could not be read: " + err.Error()
			continue
		}
		if events, err := os.ReadFile(filepath.Join(cell.Dir, record.EventsFile)); err == nil {
			result.CellEvidence[key] = HashBytes(events)
		}
		result.Cells[key] = judgeReleaseCell(cell)
	}

	if len(result.Cells) == 0 {
		return Result{}, &NothingToJudgeError{Judge: r.Name(), Unjudged: result.Unjudged}
	}
	for _, cell := range result.Cells {
		switch cell.Verdict {
		case ReleaseReady:
		case Unverified:
			if result.Verdict == ReleaseReady {
				result.Verdict = Unverified
			}
		default:
			result.Verdict = ReleaseBlocked
		}
	}
	if len(result.Unjudged) > 0 && result.Verdict == ReleaseReady {
		result.Verdict = Unverified
	}
	if len(result.Unjudged) == 0 {
		result.Unjudged = nil
	}
	return result, nil
}

var releaseFactKeys = []string{
	"retry.rc",
	"retry.refused",
	"restart.unit_present",
	"final.pkg",
	"final.launcher_present",
	"final.svc",
	"final.http8080",
	"final.http443",
	"final.schema",
	"diagnostic.legacy_jvm",
	"diagnostic.target_jvm",
	"diagnostic.quartz_locks_missing",
	"sbom.requested_version",
	"sbom.installed_version",
	"sbom.target_installed",
	"sbom.component_delta_added_count",
	"sbom.component_delta_changed_count",
	"sbom.component_delta_missing_count",
	"sbom.file_delta_added_count",
	"sbom.file_delta_changed_count",
	"sbom.file_delta_missing_count",
	"sbom.unowned_code_file_count",
	"suite.acceptance",
	"suite.status",
	"suite.app.version",
	"suite.runbooks",
	"suite.runbooks.failed",
	"suite.steps.failed",
	"pw.rc",
}

var requiredReleaseFacts = []string{
	"retry.rc",
	"retry.refused",
	"final.pkg",
	"final.svc",
	"final.http8080",
	"final.http443",
	"final.schema",
	"sbom.requested_version",
	"sbom.installed_version",
	"sbom.target_installed",
	"sbom.component_delta_added_count",
	"sbom.component_delta_changed_count",
	"sbom.component_delta_missing_count",
	"sbom.file_delta_added_count",
	"sbom.file_delta_changed_count",
	"sbom.file_delta_missing_count",
	"sbom.unowned_code_file_count",
}

func judgeReleaseCell(rec *record.Record) StepVerdict {
	premises, err := (Expect{}).Judge(rec)
	if err != nil {
		return StepVerdict{Verdict: Unverified, Because: []string{"the experiment's preconditions could not be checked: " + err.Error()}}
	}
	if premises.Verdict != Pass {
		why := reasons(premises)
		if len(why) == 0 {
			why = []string{"the experiment's declared preconditions did not all hold"}
		}
		return StepVerdict{Verdict: Unverified, Because: why}
	}

	facts, conflicts, err := selectedFacts(rec.Latest(), releaseFactKeys)
	if err != nil {
		return StepVerdict{Verdict: Unverified, Because: []string{"the release readings could not be decoded: " + err.Error()}}
	}
	if len(conflicts) > 0 {
		return StepVerdict{Verdict: Unverified, Because: conflicts}
	}
	var missing []string
	for _, key := range requiredReleaseFacts {
		if _, ok := factReading(facts, key); !ok {
			missing = append(missing, missingFactReason(key, facts))
		}
	}
	if len(missing) > 0 {
		return StepVerdict{Verdict: Unverified, Because: missing}
	}

	value := func(key string) string {
		v, _ := factReading(facts, key)
		return v
	}
	target := value("sbom.requested_version")
	browserAcceptance := value("suite.acceptance") == "required"
	if browserAcceptance {
		if missing := missingBrowserFacts(facts); len(missing) > 0 {
			return StepVerdict{Verdict: Unverified, Because: missing}
		}
	}

	var upgradeProblems []string
	if value("retry.rc") != "0" || value("retry.refused") != "no" {
		upgradeProblems = append(upgradeProblems, fmt.Sprintf(
			"the final package transaction did not succeed (rc=%q, refused=%q)",
			value("retry.rc"), value("retry.refused"),
		))
	}
	if value("final.pkg") != target || value("sbom.installed_version") != target || value("sbom.target_installed") != "yes" {
		upgradeProblems = append(upgradeProblems, fmt.Sprintf(
			"requested package %q, final package %q, SBOM installed version %q, target-installed=%q",
			target, value("final.pkg"), value("sbom.installed_version"), value("sbom.target_installed"),
		))
	}
	if len(upgradeProblems) > 0 {
		return StepVerdict{Verdict: UpgradeFailed, Because: upgradeProblems}
	}

	var managerProblems []string
	if optionalFactEquals(facts, "restart.unit_present", "no") || optionalFactEquals(facts, "final.launcher_present", "no") {
		managerProblems = append(managerProblems, "the target systemd launcher is absent")
	}
	if optionalFactEquals(facts, "diagnostic.target_jvm", "no") {
		managerProblems = append(managerProblems, "no target JVM is running")
	}
	if optionalFactEquals(facts, "diagnostic.legacy_jvm", "yes") && optionalFactEquals(facts, "diagnostic.target_jvm", "no") {
		managerProblems = append(managerProblems, "the legacy JVM remains running instead")
	}
	if len(managerProblems) > 0 {
		return StepVerdict{Verdict: ManagerTransitionFailed, Because: managerProblems}
	}

	var payloadProblems []string
	for _, key := range []string{
		"sbom.component_delta_added_count",
		"sbom.component_delta_changed_count",
		"sbom.component_delta_missing_count",
		"sbom.file_delta_added_count",
		"sbom.file_delta_changed_count",
		"sbom.file_delta_missing_count",
		"sbom.unowned_code_file_count",
	} {
		if value(key) != "0" {
			payloadProblems = append(payloadProblems, fmt.Sprintf("%s is %q, expected %q", key, value(key), "0"))
		}
	}
	if len(payloadProblems) > 0 {
		return StepVerdict{Verdict: PayloadDrift, Because: payloadProblems}
	}

	var applicationProblems []string
	if value("final.svc") != "active" {
		applicationProblems = append(applicationProblems, fmt.Sprintf("the final service state is %q, expected %q", value("final.svc"), "active"))
	}
	if !healthyHTTP(value("final.http8080")) && !healthyHTTP(value("final.http443")) {
		applicationProblems = append(applicationProblems, fmt.Sprintf(
			"the application is unavailable (HTTP 8080=%q, 443=%q)",
			value("final.http8080"), value("final.http443"),
		))
	}
	if !schemaMatchesTarget(value("final.schema"), target) {
		applicationProblems = append(applicationProblems, fmt.Sprintf(
			"database schema %q is not compatible with target %q", value("final.schema"), target,
		))
	}
	if optionalFactEquals(facts, "diagnostic.quartz_locks_missing", "yes") {
		applicationProblems = append(applicationProblems, "application startup reports the Quartz lock table missing")
	}
	if browserAcceptance {
		applicationProblems = append(applicationProblems, browserProblems(facts, target)...)
	}
	if len(applicationProblems) > 0 {
		return StepVerdict{Verdict: ApplicationFailed, Because: applicationProblems}
	}

	because := []string{
		fmt.Sprintf("package %s is installed and its service is active", target),
		fmt.Sprintf("the application answers HTTP (8080=%s, 443=%s) on schema %s", value("final.http8080"), value("final.http443"), value("final.schema")),
		"the installed payload matches the target RPM with no unowned code",
	}
	if browserAcceptance {
		because = append(because, fmt.Sprintf("the browser smoke passed against application version %s", value("suite.app.version")))
	}
	return StepVerdict{
		Verdict: ReleaseReady,
		Because: because,
	}
}

func positiveInteger(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

var browserFactKeys = []string{
	"suite.acceptance",
	"suite.status",
	"suite.app.version",
	"suite.runbooks",
	"suite.runbooks.failed",
	"suite.steps.failed",
	"pw.rc",
}

func missingBrowserFacts(facts map[string]record.Fact) []string {
	var missing []string
	for _, key := range browserFactKeys[1:] {
		if _, ok := factReading(facts, key); !ok {
			missing = append(missing, missingFactReason(key, facts))
		}
	}
	return missing
}

func browserProblems(facts map[string]record.Fact, target string) []string {
	value := func(key string) string {
		v, _ := factReading(facts, key)
		return v
	}
	var problems []string
	if value("pw.rc") != "0" || value("suite.status") != "passed" {
		problems = append(problems, fmt.Sprintf(
			"the browser smoke did not pass (Playwright rc=%q, report status=%q)",
			value("pw.rc"), value("suite.status"),
		))
	}
	if value("suite.app.version") != target {
		problems = append(problems, fmt.Sprintf(
			"the browser reached application version %q, expected %q",
			value("suite.app.version"), target,
		))
	}
	if !positiveInteger(value("suite.runbooks")) {
		problems = append(problems, "the browser report contains no runbook")
	}
	if value("suite.runbooks.failed") != "0" || value("suite.steps.failed") != "0" {
		problems = append(problems, fmt.Sprintf(
			"the browser report contains failures (runbooks=%q, steps=%q)",
			value("suite.runbooks.failed"), value("suite.steps.failed"),
		))
	}
	return problems
}

func selectedFacts(rec *record.Record, keys []string) (map[string]record.Fact, []string, error) {
	steps, order, err := gather(rec)
	if err != nil {
		return nil, nil, err
	}
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}

	facts := map[string]record.Fact{}
	owners := map[string]string{}
	var conflicts []string
	for _, n := range order {
		step := steps[n]
		if step.status != record.StatusMeasured {
			continue
		}
		for key, fact := range step.facts {
			if !wanted[key] {
				continue
			}
			if owner, exists := owners[key]; exists && owner != step.label {
				conflicts = append(conflicts, fmt.Sprintf("%s was read by both %q and %q", key, owner, step.label))
				continue
			}
			facts[key] = fact
			owners[key] = step.label
		}
	}
	sort.Strings(conflicts)
	return facts, conflicts, nil
}

func missingFactReason(key string, facts map[string]record.Fact) string {
	why := "the record contains no reading"
	if fact, exists := facts[key]; exists && fact.Error != "" {
		why = fact.Error
	}
	return key + ": " + why
}

func optionalFactEquals(facts map[string]record.Fact, key, expected string) bool {
	value, ok := factReading(facts, key)
	return ok && value == expected
}

func healthyHTTP(code string) bool {
	return len(code) == 3 && (code[0] == '2' || code[0] == '3')
}

// schemaMatchesTarget says whether a recorded Flyway schema version belongs to
// the target release. Flyway numbers migrations by release LINE, so a target
// of 7.2.3 lands on 7.2.3.3 and a four-part hotfix target of 7.3.2.1 lands on
// 7.3.2.11 - the eleventh migration of the 7.3.2 line, not a suffix of
// "7.3.2.1". A plain prefix test therefore rejected every clean 7.3.2.1 cell.
// The rule is: the schema shares the target's first three components and is
// not below the target. Anything non-numeric falls back to the prefix test.
func schemaMatchesTarget(schema, target string) bool {
	if schema == target || strings.HasPrefix(schema, target+".") {
		return true
	}
	s, sok := versionComponents(schema)
	t, tok := versionComponents(target)
	if !sok || !tok || len(s) == 0 || len(t) == 0 {
		return false
	}
	line := min(3, len(t))
	if len(s) < line {
		return false
	}
	for i := 0; i < line; i++ {
		if s[i] != t[i] {
			return false
		}
	}
	return compareVersions(s, t) >= 0
}

// versionComponents parses "7.3.2.11" into [7 3 2 11]; false if any part is
// not a plain integer.
func versionComponents(v string) ([]int, bool) {
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// compareVersions orders component lists numerically, treating a missing
// trailing component as zero.
func compareVersions(a, b []int) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
