package publish_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/publish"
)

// A finding's evidence is a quoted line and a run id. Run directories
// accumulate without bound and are not kept in the corpus, so that id resolves
// to a directory on exactly one machine - which is fine until somebody who is
// not sitting at that machine reads the finding.
//
// What gets published is what is CITED, and nothing else. Not an archive: the
// requirement is that a citation resolves, and being cited is also what keeps
// the published set honest - evidence somebody relied on, rather than
// everything the lab happened to produce.

func corpus(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func runs(t *testing.T, ids ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, id := range ids {
		if err := os.MkdirAll(filepath.Join(dir, id), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(dir, id, "events.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write events: %v", err)
		}
	}
	return dir
}

func TestEveryCitedIdIsFound(t *testing.T) {
	t.Parallel()

	found := publish.Cited(corpus(t, map[string]string{
		"one.md": "measured 2026-08-08, run aborted-upgrade-20260808T023158Z, and again\n" +
			"in version-sweep-20260812T174051Z which is a different question.\n",
		"two.md": "see aborted-upgrade-20260808T023158Z (same run, cited twice)\n",
	}))

	var ids []string
	for _, c := range found {
		ids = append(ids, c.RunID)
	}
	got := strings.Join(ids, " ")
	if !strings.Contains(got, "aborted-upgrade-20260808T023158Z") ||
		!strings.Contains(got, "version-sweep-20260812T174051Z") {
		t.Errorf("cited = %v", ids)
	}
	if len(found) != 2 {
		t.Errorf("cited = %v, want each id once however often it appears", ids)
	}
}

func TestACitationSaysWhichFileItCameFrom(t *testing.T) {
	t.Parallel()

	found := publish.Cited(corpus(t, map[string]string{
		"finding.md": "run aborted-upgrade-20260808T023158Z\n",
	}))
	if len(found) != 1 {
		t.Fatalf("cited = %v", found)
	}
	if !strings.HasSuffix(found[0].Sources[0], "finding.md") {
		t.Errorf("source = %q, want the file that cites it", found[0].Sources[0])
	}
}

func TestABareTimestampIsADefectRatherThanACitation(t *testing.T) {
	t.Parallel()

	// The prefix is part of the id. A bare timestamp names a run only if you
	// already know which scenario produced it, which is exactly the knowledge
	// a reader outside the lab does not have.
	found := publish.Cited(corpus(t, map[string]string{
		"finding.md": "measured at 20260808T023158Z, which names nothing\n",
	}))
	if len(found) != 0 {
		t.Errorf("cited = %v, want a bare timestamp not to count as an id", found)
	}

	defects := publish.Defects(corpus(t, map[string]string{
		"finding.md": "measured at 20260808T023158Z, which names nothing\n",
	}))
	if len(defects) != 1 {
		t.Fatalf("defects = %v, want the bare timestamp reported", defects)
	}
	if !strings.Contains(defects[0].Text, "20260808T023158Z") {
		t.Errorf("defect = %+v", defects[0])
	}
}

func TestATimestampInsideAFullIdIsNotAlsoADefect(t *testing.T) {
	t.Parallel()

	if got := publish.Defects(corpus(t, map[string]string{
		"finding.md": "run aborted-upgrade-20260808T023158Z\n",
	})); len(got) != 0 {
		t.Errorf("defects = %v, want none: the id names its scenario", got)
	}
}

func TestAnIdIsNotMatchedOutOfTheMiddleOfSomethingElse(t *testing.T) {
	t.Parallel()

	// A citation has to be a whole word, or a URL with an id in it would be
	// scraped as a differently-named run that does not exist.
	found := publish.Cited(corpus(t, map[string]string{
		"finding.md": "https://example.invalid/runs/aborted-upgrade-20260808T023158Z\n",
	}))
	if len(found) != 1 || found[0].RunID != "aborted-upgrade-20260808T023158Z" {
		t.Errorf("cited = %v, want the id itself", found)
	}
}

func TestResolvingLooksInEveryPlaceRunsLand(t *testing.T) {
	t.Parallel()

	// The runner writes beside the caller, so a run taken from a shell and one
	// taken from a CI workspace land in different trees.
	first := runs(t, "aborted-upgrade-20260808T023158Z")
	second := runs(t, "version-sweep-20260812T174051Z")

	plan, err := publish.Prepare(corpus(t, map[string]string{
		"finding.md": "aborted-upgrade-20260808T023158Z and version-sweep-20260812T174051Z\n",
	}), []string{first, second})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Resolved) != 2 {
		t.Errorf("resolved = %d, want both", len(plan.Resolved))
	}
	if len(plan.Missing) != 0 {
		t.Errorf("missing = %v", plan.Missing)
	}
}

func TestACitationThatResolvesToNothingIsReported(t *testing.T) {
	t.Parallel()

	// An unresolvable citation is a defect in the corpus, not something to
	// skip quietly: the finding claims evidence that nobody can reach.
	plan, err := publish.Prepare(corpus(t, map[string]string{
		"finding.md": "run gone-20260808T023158Z\n",
	}), []string{runs(t)})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Missing) != 1 || plan.Missing[0].RunID != "gone-20260808T023158Z" {
		t.Errorf("missing = %v", plan.Missing)
	}
	if plan.Complete() {
		t.Error("a plan with an unreachable citation called itself complete")
	}
}

func TestStagingCopiesOnlyWhatIsCited(t *testing.T) {
	t.Parallel()

	root := runs(t, "cited-20260808T023158Z", "uncited-20260808T023159Z")
	repo := t.TempDir()

	plan, err := publish.Prepare(corpus(t, map[string]string{
		"finding.md": "run cited-20260808T023158Z\n",
	}), []string{root})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	staged, err := publish.Stage(repo, plan)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(staged) != 1 || staged[0] != "cited-20260808T023158Z" {
		t.Errorf("staged = %v", staged)
	}
	if _, err := os.Stat(filepath.Join(repo, "uncited-20260808T023159Z")); !os.IsNotExist(err) {
		t.Error("an uncited run was published anyway")
	}
	if _, err := os.Stat(filepath.Join(repo, "cited-20260808T023158Z", "events.jsonl")); err != nil {
		t.Errorf("the published run has no evidence in it: %v", err)
	}
}

func TestStagingTwiceChangesNothingTheSecondTime(t *testing.T) {
	t.Parallel()

	root := runs(t, "cited-20260808T023158Z")
	repo := t.TempDir()
	dir := corpus(t, map[string]string{"finding.md": "run cited-20260808T023158Z\n"})

	plan, err := publish.Prepare(dir, []string{root})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := publish.Stage(repo, plan); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	// A published record is immutable once written. Re-publishing must not
	// rewrite one: the copy in the corpus is what a finding cites.
	marker := filepath.Join(repo, "cited-20260808T023158Z", "events.jsonl")
	if err := os.WriteFile(marker, []byte("{\"published\":true}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	again, err := publish.Stage(repo, plan)
	if err != nil {
		t.Fatalf("Stage again: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("staged = %v, want nothing new", again)
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "published") {
		t.Error("re-publishing overwrote a record already in the corpus")
	}
}

func TestACitedVerdictHasToBeThere(t *testing.T) {
	t.Parallel()

	// A finding cites (run-id, judge@version), and the verdict file it names
	// ships with the record. One that is missing makes the finding
	// unreproducible even though the run resolved.
	root := runs(t, "cited-20260808T023158Z")
	dir := corpus(t, map[string]string{
		"finding.md": "run cited-20260808T023158Z, judged expect@1\n",
	})

	plan, err := publish.Prepare(dir, []string{root})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Defects) == 0 {
		t.Fatal("a cited verdict that is not in the record was not reported")
	}
	if !strings.Contains(plan.Defects[0].Why, "expect@1") {
		t.Errorf("defect = %+v, want it to name the verdict", plan.Defects[0])
	}
}

func TestAVerdictThatIsThereIsNotADefect(t *testing.T) {
	t.Parallel()

	root := runs(t, "cited-20260808T023158Z")
	if err := os.MkdirAll(filepath.Join(root, "cited-20260808T023158Z", "verdicts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "cited-20260808T023158Z", "verdicts", "expect@1.json"),
		[]byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	plan, err := publish.Prepare(corpus(t, map[string]string{
		"finding.md": "run cited-20260808T023158Z, judged expect@1\n",
	}), []string{root})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Defects) != 0 {
		t.Errorf("defects = %+v", plan.Defects)
	}
}
