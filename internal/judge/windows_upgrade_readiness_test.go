package judge_test

import (
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

func TestWindowsUpgradeReadinessSeparatesSuccessFromDesktopBlock(t *testing.T) {
	t.Parallel()

	blocked := windowsUpgradeFacts()
	blocked = replaceFacts(
		blocked,
		fact("before.product.version", "6.2.4"),
		fact("installer.action311.executed", "yes"),
		fact("installer.headless_exception", "yes"),
		fact("installer.rolling_back", "yes"),
		fact("installer.reported_success", "no"),
		fact("after.product.version", "6.2.4"),
		fact("after.pg.server_major", "14"),
	)
	rec := readinessCensus(
		t,
		readinessCell{key: "7.1.3", facts: windowsUpgradeFacts()},
		readinessCell{key: "6.2.4", facts: blocked},
	)
	result, err := (judge.WindowsUpgradeReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["7.1.3"].Verdict; got != judge.ReleaseReady {
		t.Errorf("successful cell = %q, want %q: %v", got, judge.ReleaseReady, result.Cells["7.1.3"].Because)
	}
	if got := result.Cells["6.2.4"].Verdict; got != judge.InteractiveRequired {
		t.Errorf("blocked cell = %q, want %q: %v", got, judge.InteractiveRequired, result.Cells["6.2.4"].Because)
	}
	if result.Verdict != judge.ReleaseBlocked {
		t.Errorf("census = %q, want %q", result.Verdict, judge.ReleaseBlocked)
	}
	if result.CellEvidence["6.2.4"] == "" {
		t.Error("blocked cell has no pinned evidence hash")
	}
}

func TestWindowsUpgradeReadinessNamesUnknownAndApplicationFailures(t *testing.T) {
	t.Parallel()

	badApp := windowsUpgradeFacts()
	badApp = replaceFacts(
		badApp,
		fact("after.pg.ready", "no"),
		fact("after.marker.rows", "no-reading"),
		fact("after.tomcat", "Stopped"),
		fact("after.http8080", "unreachable"),
		fact("after.http443", "unreachable"),
	)
	missing := removeFact(windowsUpgradeFacts(), "installer.rolling_back")
	rec := readinessCensus(
		t,
		readinessCell{key: "bad-app", facts: badApp},
		readinessCell{key: "missing", facts: missing},
	)
	result, err := (judge.WindowsUpgradeReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["bad-app"].Verdict; got != judge.ApplicationFailed {
		t.Errorf("bad app = %q, want %q: %v", got, judge.ApplicationFailed, result.Cells["bad-app"].Because)
	}
	if why := strings.Join(result.Cells["bad-app"].Because, "; "); !strings.Contains(why, "PostgreSQL") || !strings.Contains(why, "Tomcat") || !strings.Contains(why, "HTTP") {
		t.Errorf("bad app reasons = %q", why)
	}
	if got := result.Cells["missing"].Verdict; got != judge.Unverified {
		t.Errorf("missing reading = %q, want %q", got, judge.Unverified)
	}
}

func TestWindowsUpgradeReadinessRequiresAndJudgesDeclaredBrowserAcceptance(t *testing.T) {
	t.Parallel()

	ready := append(windowsUpgradeFacts(), windowsBrowserFacts()...)
	failed := replaceFacts(ready, fact("pw.rc", "1"), fact("suite.status", "failed"), fact("suite.steps.failed", "1"))
	missing := removeFact(ready, "suite.status")
	rec := readinessCensus(
		t,
		readinessCell{key: "ready", facts: ready},
		readinessCell{key: "failed", facts: failed},
		readinessCell{key: "missing", facts: missing},
	)
	result, err := (judge.WindowsUpgradeReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["ready"].Verdict; got != judge.ReleaseReady {
		t.Errorf("ready browser cell = %q, want %q: %v", got, judge.ReleaseReady, result.Cells["ready"].Because)
	}
	if got := result.Cells["failed"].Verdict; got != judge.ApplicationFailed {
		t.Errorf("failed browser cell = %q, want %q: %v", got, judge.ApplicationFailed, result.Cells["failed"].Because)
	}
	if got := result.Cells["missing"].Verdict; got != judge.Unverified {
		t.Errorf("missing browser reading = %q, want %q: %v", got, judge.Unverified, result.Cells["missing"].Because)
	}
}

func TestWindowsUpgradeReadinessSeparatesInstalledBuildFromApplicationVersion(t *testing.T) {
	t.Parallel()

	ready := append(windowsUpgradeFacts(), windowsBrowserFacts()...)
	ready = replaceFacts(
		ready,
		fact("target.version", "7.2.1-patched"),
		fact("after.product.version", "7.2.1-patched"),
		fact("suite.app.version", "7.2.1"),
	)
	explicitMismatch := append([]record.Fact{}, ready...)
	explicitMismatch = append(explicitMismatch, fact("target.application.version", "7.2.2"))
	rec := readinessCensus(
		t,
		readinessCell{key: "derived", facts: ready},
		readinessCell{key: "explicit-mismatch", facts: explicitMismatch},
	)
	result, err := (judge.WindowsUpgradeReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["derived"].Verdict; got != judge.ReleaseReady {
		t.Errorf("derived application version = %q, want %q: %v", got, judge.ReleaseReady, result.Cells["derived"].Because)
	}
	if got := result.Cells["explicit-mismatch"].Verdict; got != judge.ApplicationFailed {
		t.Errorf("explicit application mismatch = %q, want %q: %v", got, judge.ApplicationFailed, result.Cells["explicit-mismatch"].Because)
	}
}

func TestWindowsUpgradeReadinessJudgesPre624LicenseTransition(t *testing.T) {
	t.Parallel()

	ready := append(windowsUpgradeFacts(), windowsBrowserFacts()...)
	ready = append(ready, windowsLicenseTransitionFacts()...)
	failed := replaceFacts(
		ready,
		fact("suite.license_transition.rc", "1"),
		fact("final.license.magic", "2d2d"),
		fact("pw.rc", "1"),
		fact("suite.status", "failed"),
		fact("suite.steps.failed", "1"),
	)
	missing := removeFact(ready, "carried.license.magic")
	rec := readinessCensus(
		t,
		readinessCell{key: "ready", facts: ready},
		readinessCell{key: "replacement-failed", facts: failed},
		readinessCell{key: "missing", facts: missing},
	)
	result, err := (judge.WindowsUpgradeReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["ready"].Verdict; got != judge.ReleaseReady {
		t.Errorf("ready transition = %q, want %q: %v", got, judge.ReleaseReady, result.Cells["ready"].Because)
	}
	if why := strings.Join(result.Cells["ready"].Because, "; "); !strings.Contains(why, "ZIP-format") {
		t.Errorf("ready transition reasons = %q", why)
	}
	if got := result.Cells["replacement-failed"].Verdict; got != judge.ApplicationFailed {
		t.Errorf("failed transition = %q, want %q: %v", got, judge.ApplicationFailed, result.Cells["replacement-failed"].Because)
	}
	if why := strings.Join(result.Cells["replacement-failed"].Because, "; "); !strings.Contains(why, "licence replacement failed") {
		t.Errorf("failed transition reasons = %q", why)
	}
	if got := result.Cells["missing"].Verdict; got != judge.Unverified {
		t.Errorf("missing transition reading = %q, want %q: %v", got, judge.Unverified, result.Cells["missing"].Because)
	}
}

func windowsUpgradeFacts() []record.Fact {
	return []record.Fact{
		fact("pre.ready", "yes"),
		fact("before.product.version", "7.1.3"),
		fact("before.pg.ready", "yes"),
		fact("before.marker.rows", "3"),
		fact("before.media.present", "yes"),
		fact("before.media.sha256", "2a64c8fc"),
		fact("before.media.bytes", "994353464"),
		fact("target.version", "7.2.1"),
		fact("target.pg.server_major", "17"),
		fact("varfile.confirmed_update", "yes"),
		fact("quiesce.processes_left", "0"),
		fact("quiesce.jre_unlocked", "yes"),
		fact("installer.exit", "0"),
		fact("installer.timed_out", "no"),
		fact("installer.action311.executed", "no"),
		fact("installer.headless_exception", "no"),
		fact("installer.rolling_back", "no"),
		fact("installer.reported_success", "yes"),
		fact("after.product.version", "7.2.1"),
		fact("after.pg.server_major", "17"),
		fact("after.pg.ready", "yes"),
		fact("after.marker.rows", "3"),
		fact("after.schema", "7.2.1.1"),
		fact("after.http8080", "200"),
		fact("after.http443", "unreachable"),
		fact("after.tomcat", "Running"),
	}
}

func windowsBrowserFacts() []record.Fact {
	return []record.Fact{
		fact("suite.acceptance", "required"),
		fact("suite.status", "passed"),
		fact("suite.app.version", "7.2.1"),
		fact("suite.runbooks", "1"),
		fact("suite.runbooks.failed", "0"),
		fact("suite.steps.failed", "0"),
		fact("pw.rc", "0"),
	}
}

func windowsLicenseTransitionFacts() []record.Fact {
	return []record.Fact{
		fact("suite.license_transition.required", "yes"),
		fact("suite.license_transition.rc", "0"),
		fact("source.license.magic", "2d2d"),
		fact("carried.license.magic", "2d2d"),
		fact("final.license.magic", "504b"),
	}
}
