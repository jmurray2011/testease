package judge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jmurray2011/testease/internal/record"
)

const InteractiveRequired = "interactive-required"

// WindowsUpgradeReadiness derives a product verdict from the Windows installer
// census. The installer exit code is deliberately only one reading: install4j
// can return zero after a component failed or a rollback started.
type WindowsUpgradeReadiness struct{}

var _ Judge = WindowsUpgradeReadiness{}

func (WindowsUpgradeReadiness) Name() string    { return "windows-upgrade-readiness" }
func (WindowsUpgradeReadiness) Version() string { return "3" }

func (w WindowsUpgradeReadiness) Judge(rec *record.Record) (Result, error) {
	keys, statuses, err := cells(rec)
	if err != nil {
		return Result{}, err
	}
	if len(keys) == 0 {
		return Result{}, &NothingToJudgeError{Judge: w.Name()}
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
		result.Cells[key] = judgeWindowsUpgradeCell(cell)
	}

	if len(result.Cells) == 0 {
		return Result{}, &NothingToJudgeError{Judge: w.Name(), Unjudged: result.Unjudged}
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

var requiredWindowsUpgradeFactKeys = []string{
	"before.product.version",
	"before.pg.ready",
	"before.marker.rows",
	"before.media.present",
	"before.media.sha256",
	"before.media.bytes",
	"target.version",
	"target.pg.server_major",
	"varfile.confirmed_update",
	"quiesce.processes_left",
	"quiesce.jre_unlocked",
	"installer.exit",
	"installer.timed_out",
	"installer.action311.executed",
	"installer.headless_exception",
	"installer.rolling_back",
	"installer.reported_success",
	"after.product.version",
	"after.pg.server_major",
	"after.pg.ready",
	"after.marker.rows",
	"after.schema",
	"after.http8080",
	"after.http443",
	"after.tomcat",
}

var licenseTransitionFactKeys = []string{
	"suite.license_transition.required",
	"suite.license_transition.rc",
	"source.license.magic",
	"carried.license.magic",
	"final.license.magic",
}

var windowsUpgradeFactKeys = append(
	append(append(append([]string{}, requiredWindowsUpgradeFactKeys...), browserFactKeys...), licenseTransitionFactKeys...),
	"target.application.version",
)

var windowsBuildSuffix = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)(?:[-+].+)$`)

func windowsApplicationVersion(facts map[string]record.Fact, installedVersion string) string {
	if configured, ok := factReading(facts, "target.application.version"); ok {
		return configured
	}
	if match := windowsBuildSuffix.FindStringSubmatch(installedVersion); match != nil {
		return match[1]
	}
	return installedVersion
}

func judgeWindowsUpgradeCell(rec *record.Record) StepVerdict {
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

	facts, conflicts, err := selectedFacts(rec.Latest(), windowsUpgradeFactKeys)
	if err != nil {
		return StepVerdict{Verdict: Unverified, Because: []string{"the Windows upgrade readings could not be decoded: " + err.Error()}}
	}
	if len(conflicts) > 0 {
		return StepVerdict{Verdict: Unverified, Because: conflicts}
	}
	for _, key := range requiredWindowsUpgradeFactKeys {
		if _, ok := factReading(facts, key); !ok {
			return StepVerdict{Verdict: Unverified, Because: []string{missingFactReason(key, facts)}}
		}
	}
	value := func(key string) string {
		v, _ := factReading(facts, key)
		return v
	}
	browserAcceptance := value("suite.acceptance") == "required"
	if browserAcceptance {
		if missing := missingBrowserFacts(facts); len(missing) > 0 {
			return StepVerdict{Verdict: Unverified, Because: missing}
		}
	}
	licenseTransition := value("suite.license_transition.required") == "yes"
	if licenseTransition {
		for _, key := range licenseTransitionFactKeys[1:] {
			if _, ok := factReading(facts, key); !ok {
				return StepVerdict{Verdict: Unverified, Because: []string{missingFactReason(key, facts)}}
			}
		}
	}

	before := value("before.product.version")
	target := value("target.version")
	final := value("after.product.version")
	marker := value("after.marker.rows")

	if value("installer.timed_out") == "yes" {
		return StepVerdict{Verdict: UpgradeFailed, Because: []string{"the installer timed out"}}
	}
	if value("installer.action311.executed") == "yes" &&
		value("installer.headless_exception") == "yes" &&
		final == before && marker == value("before.marker.rows") {
		return StepVerdict{
			Verdict: InteractiveRequired,
			Because: []string{
				"the unattended installer reached PostgreSQL major-upgrade action 311, called Swing without a desktop, and aborted",
				fmt.Sprintf("the source release %s and its %s marker rows remained intact", before, marker),
			},
		}
	}

	var upgradeProblems []string
	if final != target {
		upgradeProblems = append(upgradeProblems, fmt.Sprintf("requested release %q, final registered release %q", target, final))
	}
	if value("installer.reported_success") != "yes" || value("installer.rolling_back") != "no" || value("installer.headless_exception") != "no" {
		upgradeProblems = append(upgradeProblems, fmt.Sprintf(
			"installer reported-success=%q, rollback=%q, headless-exception=%q, exit=%q",
			value("installer.reported_success"), value("installer.rolling_back"),
			value("installer.headless_exception"), value("installer.exit"),
		))
	}
	if len(upgradeProblems) > 0 {
		return StepVerdict{Verdict: UpgradeFailed, Because: upgradeProblems}
	}

	var applicationProblems []string
	if value("after.pg.server_major") != value("target.pg.server_major") {
		applicationProblems = append(applicationProblems, fmt.Sprintf(
			"PostgreSQL major %q is running, expected %q", value("after.pg.server_major"), value("target.pg.server_major"),
		))
	}
	if value("after.pg.ready") != "yes" {
		applicationProblems = append(applicationProblems, "the final PostgreSQL service is not ready")
	}
	if marker != value("before.marker.rows") {
		applicationProblems = append(applicationProblems, fmt.Sprintf(
			"the seeded marker changed from %q rows to %q", value("before.marker.rows"), marker,
		))
	}
	if value("after.tomcat") != "Running" {
		applicationProblems = append(applicationProblems, fmt.Sprintf("the final Tomcat service state is %q", value("after.tomcat")))
	}
	if !healthyHTTP(value("after.http8080")) && !healthyHTTP(value("after.http443")) {
		applicationProblems = append(applicationProblems, fmt.Sprintf(
			"the application is unavailable (HTTP 8080=%q, 443=%q)", value("after.http8080"), value("after.http443"),
		))
	}
	if browserAcceptance {
		applicationProblems = append(applicationProblems, browserProblems(facts, windowsApplicationVersion(facts, target))...)
	}
	if licenseTransition {
		sourceMagic := value("source.license.magic")
		carriedMagic := value("carried.license.magic")
		finalMagic := value("final.license.magic")
		if sourceMagic == "504b" {
			applicationProblems = append(applicationProblems, "the source already held a ZIP-format licence, so the pre-6.2.4 boundary was not exercised")
		}
		if carriedMagic != sourceMagic {
			applicationProblems = append(applicationProblems, fmt.Sprintf(
				"the licence bytes changed during installation (source magic %q, carried magic %q)", sourceMagic, carriedMagic,
			))
		}
		if value("suite.license_transition.rc") != "0" {
			applicationProblems = append(applicationProblems, fmt.Sprintf(
				"the post-upgrade licence replacement failed with browser exit %q", value("suite.license_transition.rc"),
			))
		}
		if finalMagic != "504b" {
			applicationProblems = append(applicationProblems, fmt.Sprintf(
				"the latest stored licence has magic %q, expected ZIP magic %q", finalMagic, "504b",
			))
		}
	}
	if len(applicationProblems) > 0 {
		return StepVerdict{Verdict: ApplicationFailed, Because: applicationProblems}
	}

	because := []string{
		fmt.Sprintf("Windows release %s replaced %s without rollback", target, before),
		fmt.Sprintf("PostgreSQL %s is ready and retained all %s marker rows", value("after.pg.server_major"), marker),
		fmt.Sprintf("Tomcat is running and the application answers HTTP (8080=%s, 443=%s)", value("after.http8080"), value("after.http443")),
	}
	if browserAcceptance {
		because = append(because, fmt.Sprintf("the browser smoke passed against application version %s", value("suite.app.version")))
	}
	if licenseTransition {
		because = append(because, "the old licence survived installation and the application accepted a new ZIP-format licence afterward")
	}
	return StepVerdict{
		Verdict: ReleaseReady,
		Because: because,
	}
}
