package runner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// Scope is the set of cells a census covers, and how that set was arrived at.
//
// How scope was determined is recorded, not just what it was. Discovery is
// host-dependent: two people with different artifact stores get differently
// scoped sweeps whose records read identically unless the set is written down.
type Scope struct {
	// Axes maps an axis name to its values, in the order they will run.
	Axes map[string][]string

	// Exclude removes combinations that are known to be invalid or not useful
	// without making authors encode a whole relationship as one opaque axis.
	// An entry matches when every name/value pair in it matches a cell.
	Exclude []map[string]string

	// Source says where the values came from - a command line, or discovery
	// of some kind.
	Source string

	// DiscoveredOn names the machine that enumerated them, when that was not
	// simply the caller typing them out.
	DiscoveredOn string
}

// Cell is one cell of a census: the key it is filed under and the parameter
// values that make it.
type Cell struct {
	Key    string
	Params map[string]string
}

// keySafe is the character set a cell key may use. It is also a directory
// name under cells/, so it has to survive being one.
//
// A key is an identifier, not a parseable tuple. `+` joins multi-axis values
// and is also legal inside one, so a key cannot be split back into the values
// that made it - the axes and their order live in the record's matrix scope,
// which is where anything needing them should look.
var keySafe = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

// Cells enumerates the scope, in order.
//
// Axes are taken in name order so that a census enumerates the same way twice;
// values keep the order they were given, because that is the order somebody
// chose to sweep in. The first axis varies slowest, so a multi-axis census
// reads down its primary axis.
func (s Scope) Cells() ([]Cell, error) {
	names := make([]string, 0, len(s.Axes))
	for name := range s.Axes {
		names = append(names, name)
	}
	sort.Strings(names)

	cells := []Cell{{Params: map[string]string{}}}
	for _, name := range names {
		var next []Cell
		for _, cell := range cells {
			for _, value := range s.Axes[name] {
				safe := sanitize(value)
				if safe == "" {
					// A key part is a directory name, and a value that leaves
					// nothing behind would file the cell nowhere - or, joined
					// with another axis, under a key that lies about it.
					return nil, fmt.Errorf(
						"axis %q value %q has no characters a cell key can hold, so the cell cannot be filed",
						name, value,
					)
				}
				params := make(map[string]string, len(cell.Params)+1)
				for k, v := range cell.Params {
					params[k] = v
				}
				params[name] = value
				if excluded(params, s.Exclude) {
					continue
				}

				key := cell.Key
				if key != "" {
					key += "+"
				}
				next = append(next, Cell{Key: key + safe, Params: params})
			}
		}
		cells = next
	}

	if len(names) == 0 {
		return nil, nil
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("matrix exclusions remove every cell")
	}
	if err := distinct(cells); err != nil {
		return nil, err
	}
	return cells, nil
}

func excluded(params map[string]string, exclusions []map[string]string) bool {
	for _, exclusion := range exclusions {
		matches := true
		for name, value := range exclusion {
			if params[name] != value {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

// distinct refuses a scope whose cells cannot be told apart.
//
// Sanitising a key is lossy, so two different values can land on one key - and
// two cells sharing a key write their evidence into one directory, where the
// second silently replaces the first. Refusing the scope costs a run that
// never starts; not refusing it costs a census nobody can trust.
func distinct(cells []Cell) error {
	seen := make(map[string]map[string]string, len(cells))
	for _, cell := range cells {
		if first, clash := seen[cell.Key]; clash {
			return fmt.Errorf(
				"two cells share the key %q (%s and %s): cell keys become directory names, "+
					"so values that differ only in characters a directory name cannot hold "+
					"would write into one record",
				cell.Key, describeParams(first), describeParams(cell.Params),
			)
		}
		seen[cell.Key] = cell.Params
	}
	return nil
}

func sanitize(value string) string {
	return strings.Trim(keySafe.ReplaceAllString(value, "-"), "-")
}

func describeParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, ",")
}

// Measured is the set of cell keys a record says were measured.
//
// It is derived from the evidence and from nothing else. The alternative is a
// progress file, which is bookkeeping that can disagree with the record - and
// when it does, a census reads as complete over a set nobody measured.
//
// Only measured counts. A cell that failed its infrastructure, failed its
// probe, or was interrupted is not done, and neither is one that started and
// never finished: that cell wrote no cell-finished, which is exactly the set a
// resume must re-run, and it needs no extra bookkeeping to know it.
//
// This is a deliberate inversion of the bash it replaces, where a rung counted
// as done once it had a row of any kind, NOBOOT included.
func Measured(rec *record.Record) map[string]bool {
	done := map[string]bool{}
	for _, e := range rec.Events {
		if e.Kind != record.KindCellFinished {
			continue
		}
		finished, err := record.Decode[record.CellFinished](e)
		if err != nil {
			continue
		}
		if finished.Status == record.StatusMeasured {
			done[finished.Key] = true
		}
	}
	return done
}

// Remaining is the cells of a scope that are not done, in the scope's order.
// Keys compare whole: a measured 6.2.4 never makes 6.2.40 look done.
func Remaining(cells []Cell, done map[string]bool) []Cell {
	var out []Cell
	for _, cell := range cells {
		if done[cell.Key] {
			continue
		}
		out = append(out, cell)
	}
	return out
}

// RecordedScope is the scope a prior census wrote down.
//
// A resumed census covers the set the first pass recorded, not whatever
// discovery would find today. Re-discovering would make what a record claims
// depend on the machine that resumed it, and two hosts' idea of "everything"
// differ silently.
func RecordedScope(rec *record.Record) (Scope, error) {
	for _, e := range rec.Events {
		if e.Kind != record.KindRunStarted {
			continue
		}
		started, err := record.Decode[record.RunStarted](e)
		if err != nil {
			return Scope{}, err
		}
		if started.Matrix == nil {
			continue
		}
		return Scope{
			Axes:         started.Matrix.Axes,
			Exclude:      started.Matrix.Exclude,
			Source:       started.Matrix.Source,
			DiscoveredOn: started.Matrix.DiscoveredOn,
		}, nil
	}
	return Scope{}, fmt.Errorf("record %s is not a census: it has no matrix scope to resume", rec.Dir)
}
