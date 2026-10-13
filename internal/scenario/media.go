package scenario

import (
	"fmt"
	"strings"
)

// ParseMount reads a source:dest[:ro] mount spelling.
//
// A drive letter is not a separator. Splitting on every colon turns C:\store
// into a source called "C", which then fails as a mount nobody typed - and
// Windows paths became reachable the moment one-shot cells did. The spelling
// belongs to the manifest, so the parser lives here where everything that
// reads a manifest can share it.
func ParseMount(spec string) (source, dest string, readOnly bool) {
	rest := spec
	if strings.HasSuffix(rest, ":ro") {
		readOnly = true
		rest = strings.TrimSuffix(rest, ":ro")
	} else if strings.HasSuffix(rest, ":rw") {
		rest = strings.TrimSuffix(rest, ":rw")
	}
	source, dest = splitMount(rest)
	return source, dest, readOnly
}

// splitMount finds the one colon that separates a source from a destination,
// stepping over a leading drive letter on either side.
func splitMount(s string) (source, dest string) {
	i := afterDrive(s)
	if j := strings.Index(s[i:], ":"); j >= 0 {
		return s[:i+j], s[i+j+1:]
	}
	return s, ""
}

// afterDrive is where to start looking for a separator: past "C:" when the
// string opens with one.
func afterDrive(s string) int {
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') &&
		((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) {
		return 2
	}
	return 0
}

// BindMountSources replaces logical mount sources with runtime paths. Prefix
// distinguishes independently owned namespaces such as @inputs and %secret
// files; every other mount is left byte-for-byte as written.
func BindMountSources(plan *Plan, prefix string, sources map[string]string) error {
	bind := func(items []string, where string) ([]string, error) {
		out := make([]string, 0, len(items))
		for _, item := range items {
			source, destination, readOnly := ParseMount(item)
			if !strings.HasPrefix(source, prefix) {
				out = append(out, item)
				continue
			}
			name := strings.TrimPrefix(source, prefix)
			path, ok := sources[name]
			if !ok {
				return nil, fmt.Errorf("%s mounts %s%s, which was not bound", where, prefix, name)
			}
			if destination == "" {
				return nil, fmt.Errorf("%s mounts %s%s with no destination", where, prefix, name)
			}
			bound := path + ":" + destination
			if readOnly {
				bound += ":ro"
			}
			out = append(out, bound)
		}
		return out, nil
	}

	for name, host := range plan.Hosts {
		items, err := bind(host.Media, fmt.Sprintf("host %q", name))
		if err != nil {
			return err
		}
		host.Media = items
		plan.Hosts[name] = host
	}
	for i := range plan.Steps {
		items, err := bind(plan.Steps[i].Media, fmt.Sprintf("step %q", plan.Steps[i].Label))
		if err != nil {
			return err
		}
		plan.Steps[i].Media = items
	}
	return nil
}
