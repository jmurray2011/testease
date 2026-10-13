// Package store enumerates what a host actually has.
//
// A sweep over a fixed ladder silently skips a rung whose media is missing and
// then reports a complete census over an incomplete set. Discovering the set
// instead fixes that and introduces the opposite problem: the scope becomes
// host-dependent, and two people with different stores get differently scoped
// sweeps whose records read identically. Which is why what was found, and that
// it was found rather than typed, is written into the record.
//
// This is glob-over-directory. A store that carries its own manifest is
// adjacent work testease would consume and should not own, and when it exists
// it becomes another source here rather than a change to anything else.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Prefix marks a value that is discovered rather than given.
const Prefix = "@store:"

// IsDiscovery reports whether a value asks for discovery.
func IsDiscovery(v string) bool { return strings.HasPrefix(v, Prefix) }

// Discover lists what a store holds matching a pattern, in the order a person
// would read them.
//
// Finding nothing is refused rather than swept: a census over zero cells that
// reports itself complete is the failure discovery exists to prevent, in its
// largest form.
func Discover(dir, value string) ([]string, error) {
	pattern := strings.TrimPrefix(value, Prefix)
	if pattern == "" {
		return nil, fmt.Errorf("%q asks for discovery but says nothing to look for", value)
	}
	if filepath.IsAbs(pattern) || strings.Contains(pattern, "..") {
		return nil, fmt.Errorf("pattern %q reaches outside the store; a discovery names what is in it", pattern)
	}
	if _, err := os.Stat(dir); err != nil {
		// A store that is not there is not a store holding nothing.
		return nil, fmt.Errorf("no artifact store at %s: %w", dir, err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pattern)))
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}

	names := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(dir, m)
		if err != nil {
			continue
		}
		names = append(names, filepath.ToSlash(rel))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("nothing in %s matches %q, so there is nothing to sweep", dir, pattern)
	}

	sort.Slice(names, func(i, j int) bool { return lessVersion(names[i], names[j]) })
	return names, nil
}

// lessVersion orders version-shaped names the way a person reads them.
//
// Sorting them as text puts 10 before 2, and a census enumerated 1.0, 10.0, 2.0
// reads as though somebody chose that order. Nobody did.
func lessVersion(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		if isDigit(a[ai]) && isDigit(b[bi]) {
			an, aNext := number(a, ai)
			bn, bNext := number(b, bi)
			if an != bn {
				return an < bn
			}
			ai, bi = aNext, bNext
			continue
		}
		if a[ai] != b[bi] {
			return a[ai] < b[bi]
		}
		ai++
		bi++
	}
	return len(a)-ai < len(b)-bi
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func number(s string, i int) (int, int) {
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	n, _ := strconv.Atoi(s[i:j])
	return n, j
}
