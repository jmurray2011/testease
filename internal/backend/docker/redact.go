package docker

import "strings"

// redact prepares a docker argument list for a log line.
//
// A manifest's env: block becomes `docker exec -e KEY=VALUE`, and a scenario
// run against a real system puts credentials in it. A debug log carrying those
// values has published them to whatever collects the output, which is usually a
// CI job nobody thinks of as a secret store. The key is the useful half for
// diagnosis, so the key is what survives.
func redact(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)

	for i := 0; i < len(out)-1; i++ {
		if out[i] != "-e" && out[i] != "--env" {
			continue
		}
		key, _, found := strings.Cut(out[i+1], "=")
		if !found {
			// `-e KEY` passes a value through from this process's own
			// environment; there is nothing here to leak.
			continue
		}
		out[i+1] = key + "=<redacted>"
		i++
	}
	return out
}
