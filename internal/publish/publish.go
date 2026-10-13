// Package publish moves the run records a corpus cites to where its readers
// can reach them.
//
// The rule is one sentence and it has not changed: a run becomes publishable by
// being cited. Not every run - records accumulate without bound and the
// requirement is not an archive, it is that a citation resolves. Being cited is
// also what keeps the published set honest, because it is evidence somebody
// relied on rather than everything a lab happened to produce.
//
// Findings cite ids rather than URLs, and that is deliberate. An id is the
// durable key and a URL is a deployment detail: forty findings carrying a
// hostname all rot together the day the host moves, while the id never changes.
package publish

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// idPattern is the citation contract.
//
// The scenario prefix is part of an id, and matching is anchored on both sides
// so that an id inside a URL is still the id and a longer word containing one
// is not a citation. The stamp shape is the run id's own, from the record
// package that mints them.
var idPattern = regexp.MustCompile(
	`(^|[^A-Za-z0-9._-])([A-Za-z0-9][A-Za-z0-9._-]*-20[0-9]{6}T[0-9]{6}Z)([^A-Za-z0-9._-]|$)`,
)

// stampPattern finds a timestamp that names no scenario.
var stampPattern = regexp.MustCompile(`(^|[^A-Za-z0-9._-])(20[0-9]{6}T[0-9]{6}Z)([^A-Za-z0-9._-]|$)`)

// verdictPattern finds a judge and version cited beside a run.
var verdictPattern = regexp.MustCompile(`(^|[^A-Za-z0-9._-])([a-z][a-z0-9-]*)@([0-9]+)([^A-Za-z0-9._-]|$)`)

// Citation is a run a corpus relies on, and where it says so.
type Citation struct {
	RunID string

	// Sources are the files that cite it, in reading order. An id cited from
	// three findings is one citation with three sources: what matters is that
	// it resolves, once.
	Sources []string

	// Verdicts are judge@version pairs cited on the same line as the run. A
	// finding cites (run-id, judge@version), and the verdict file it names has
	// to ship with the record or the finding cannot be re-derived.
	Verdicts []string
}

// Defect is something in the corpus that cannot be resolved: a timestamp
// naming no scenario, a citation pointing at a record nobody has, a verdict a
// record does not carry.
//
// Defects are reported rather than skipped. A citation that resolves to
// nothing is a claim of evidence somebody cannot reach, which is worse than a
// finding that never claimed it.
type Defect struct {
	Source string
	Text   string
	Why    string
}

// Cited is every run id a corpus names, each once, in the order first seen.
func Cited(dir string) []Citation {
	byID := map[string]*Citation{}
	var order []string

	eachLine(dir, func(source, line string) {
		verdicts := verdictsOn(line)
		for _, id := range idsOn(line) {
			c, seen := byID[id]
			if !seen {
				c = &Citation{RunID: id}
				byID[id] = c
				order = append(order, id)
			}
			c.Sources = appendOnce(c.Sources, source)
			for _, v := range verdicts {
				c.Verdicts = appendOnce(c.Verdicts, v)
			}
		}
	})

	out := make([]Citation, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

// Defects are the citations a corpus makes that name nothing resolvable.
func Defects(dir string) []Defect {
	var out []Defect

	eachLine(dir, func(source, line string) {
		ids := idsOn(line)
		for _, stamp := range match(stampPattern, line) {
			// A stamp inside a full id is that id, not a bare citation.
			inside := false
			for _, id := range ids {
				if strings.HasSuffix(id, stamp) {
					inside = true
					break
				}
			}
			if inside {
				continue
			}
			out = append(out, Defect{
				Source: source,
				Text:   stamp,
				Why: "a bare timestamp names a run only to somebody who already knows which scenario " +
					"produced it, which is exactly what a reader outside the lab does not know",
			})
		}
	})
	return out
}

// Resolved is a citation and the record it was found at.
type Resolved struct {
	Citation
	Dir string
}

// Plan is what publishing would do.
type Plan struct {
	Resolved []Resolved
	Missing  []Citation
	Defects  []Defect
}

// Complete says whether every citation the corpus makes can be reached.
func (p Plan) Complete() bool { return len(p.Missing) == 0 && len(p.Defects) == 0 }

// Prepare works out what a corpus cites, where those records are, and what it
// cites that nobody can reach.
func Prepare(corpus string, roots []string) (Plan, error) {
	if _, err := os.Stat(corpus); err != nil {
		return Plan{}, fmt.Errorf("read the corpus: %w", err)
	}

	plan := Plan{Defects: Defects(corpus)}
	for _, c := range Cited(corpus) {
		dir, found := Resolve(roots, c.RunID)
		if !found {
			plan.Missing = append(plan.Missing, c)
			continue
		}
		plan.Resolved = append(plan.Resolved, Resolved{Citation: c, Dir: dir})

		for _, v := range c.Verdicts {
			name, version, _ := strings.Cut(v, "@")
			if _, err := os.Stat(filepath.Join(dir, "verdicts", name+"@"+version+".json")); err != nil {
				plan.Defects = append(plan.Defects, Defect{
					Source: strings.Join(c.Sources, ", "),
					Text:   c.RunID,
					Why: fmt.Sprintf("cites %s, and record %s carries no such verdict: "+
						"the verdict a finding names ships with the record, or the finding cannot be re-derived",
						v, c.RunID),
				})
			}
		}
	}
	return plan, nil
}

// Resolve finds a record. The runner writes beside its caller, so a run taken
// from a shell and one taken from a CI workspace land in different trees.
func Resolve(roots []string, id string) (string, bool) {
	for _, root := range roots {
		dir := filepath.Join(root, id)
		if _, err := os.Stat(filepath.Join(dir, record.EventsFile)); err == nil {
			return dir, true
		}
	}
	return "", false
}

// Stage copies the records a corpus cites into the repository its readers have,
// and reports which ones were new.
//
// A record already there is left exactly as it is. Records are immutable once
// written, and the copy in the corpus is the one a finding cites: re-publishing
// must never rewrite it.
func Stage(repo string, plan Plan) ([]string, error) {
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return nil, fmt.Errorf("make room for published records: %w", err)
	}

	var staged []string
	for _, r := range plan.Resolved {
		dest := filepath.Join(repo, r.RunID)
		if _, err := os.Stat(dest); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return staged, fmt.Errorf("look for %s: %w", r.RunID, err)
		}

		// Copied beside and renamed into place, so the published name only
		// ever holds a whole record. A copy that died midway used to leave a
		// partial tree under the run id, which the skip above then treated as
		// published forever - a citation resolving to half the evidence.
		tmp := dest + ".partial"
		_ = os.RemoveAll(tmp)
		if err := copyTree(r.Dir, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return staged, err
		}
		if err := os.Rename(tmp, dest); err != nil {
			_ = os.RemoveAll(tmp)
			return staged, fmt.Errorf("place %s: %w", r.RunID, err)
		}
		staged = append(staged, r.RunID)
	}
	sort.Strings(staged)
	return staged, nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return copyFile(p, dest)
	})
}

// copyFile streams one file across, so a record does not need to fit in memory
// to be published.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("read %s: %w", from, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", to, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("write %s: %w", to, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", to, err)
	}
	return nil
}

// --- reading the corpus ---------------------------------------------------

func eachLine(dir string, visit func(source, line string)) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable file is not a reason to stop reading the corpus
		}
		if ext := strings.ToLower(filepath.Ext(p)); ext != ".md" && ext != ".txt" {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(body), "\n") {
			visit(p, line)
		}
		return nil
	})
}

func idsOn(line string) []string { return match(idPattern, line) }

func verdictsOn(line string) []string {
	var out []string
	for _, m := range verdictPattern.FindAllStringSubmatch(line, -1) {
		out = appendOnce(out, m[2]+"@"+m[3])
	}
	return out
}

// match returns the second group of every non-overlapping match, stepping the
// search on by hand because a citation's trailing boundary is also the leading
// boundary of whatever follows it.
func match(re *regexp.Regexp, line string) []string {
	var out []string
	for i := 0; i < len(line); {
		loc := re.FindStringSubmatchIndex(line[i:])
		if loc == nil {
			break
		}
		out = appendOnce(out, line[i+loc[4]:i+loc[5]])
		i += loc[5]
	}
	return out
}

func appendOnce(list []string, v string) []string {
	for _, seen := range list {
		if seen == v {
			return list
		}
	}
	return append(list, v)
}
