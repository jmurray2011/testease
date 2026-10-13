package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// backend is one kind of host, and what a scenario can ask of it.
//
// This table is the answer every precondition failure sends people to read,
// and doctor now reads it for them - so a capability listed here that a
// backend does not have is a wrong refusal before a box is booted rather than
// a wrong sentence in a help screen.
type backendKind struct {
	Provider     string   `json:"provider"`
	Dials        string   `json:"dials"`
	Capabilities []string `json:"capabilities"`
	Creates      bool     `json:"creates"`

	// Conditional is what this kind can also offer and what each one
	// additionally needs. Separate from Capabilities because "docker can
	// fingerprint" is only true of a run that was given an instrument, and a
	// check that treated the two alike would pass a scenario the run refuses.
	Conditional map[string]string `json:"conditional,omitempty"`

	Needs string `json:"needs,omitempty"`
}

func backendKinds() []backendKind {
	return []backendKind{
		{
			Provider: "docker", Dials: "a container it boots", Creates: true,
			Capabilities: []string{"session", "media"},
			Conditional: map[string]string{
				"fingerprint": "--instrument-collect and --instrument-diff",
				"endpoint":    "the host to declare a port",
			},
		},
		{
			Provider: "endpoint", Dials: "an existing HTTP origin", Creates: false,
			Capabilities: []string{"endpoint"},
			Needs:        "an explicit http(s) origin; DNS and the TLS leaf identity are recorded",
		},
		{
			Provider: "hyperv", Dials: "one container per command", Creates: true,
			Capabilities: []string{"oneshot", "carry"},
		},
		{
			Provider: "static", Dials: "a machine already running", Creates: false,
			Capabilities: []string{"session", "media"},
			Conditional:  map[string]string{"fingerprint": "--instrument-collect and --instrument-diff"},
			Needs: "ssh, ssm or winrm, chosen by the host's backend: field; credentials come from " +
				"--aws-profile, --winrm-user, --winrm-password-file, --ssh-user, --ssh-key and\n--ssh-password-file, never from a scenario",
		},
		{
			Provider: "statefile", Dials: "what a provisioner wrote down", Creates: false,
			Capabilities: []string{"session"},
			Conditional:  map[string]string{"endpoint": "an origin in the state file"},
		},
	}
}

// backendsCmd answers the question every precondition failure sends people to
// ask.
func backendsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backends",
		Short: "what a host can be, and what each kind can do",
		Args:  cobra.NoArgs,
		RunE:  doBackends,
	}
	cmd.Flags().StringP("output", "o", "text", "text or json")
	return cmd
}

func doBackends(cmd *cobra.Command, _ []string) error {
	kinds := backendKinds()

	if flagString(cmd, "output") == "json" {
		return writeJSON(cmd, struct {
			Backends []backendKind `json:"backends"`
		}{Backends: kinds})
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "provider\tdials\tcapabilities\tcreates"); err != nil {
		return fmt.Errorf("list backends: %w", err)
	}
	for _, k := range kinds {
		creates := "no"
		if k.Creates {
			creates = "yes"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			k.Provider, k.Dials, strings.Join(offered(k), ", "), creates); err != nil {
			return fmt.Errorf("list backends: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("list backends: %w", err)
	}

	for _, k := range kinds {
		for _, need := range slices.Sorted(maps.Keys(k.Conditional)) {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n%s: %s needs %s.\n",
				k.Provider, need, k.Conditional[need]); err != nil {
				return fmt.Errorf("list backends: %w", err)
			}
		}
		if k.Needs == "" {
			continue
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n%s: %s.\n", k.Provider, k.Needs); err != nil {
			return fmt.Errorf("list backends: %w", err)
		}
	}

	// A capability the target does not have is recorded unavailable, which is a
	// statement about the target rather than a gap in the record.
	if _, err := fmt.Fprintln(cmd.OutOrStdout(),
		"\nA capability a target does not have is recorded unavailable, not omitted.\n"+
			"A drive step uses the endpoint and never the execution surface; a probe\n"+
			"uses the surface and never the endpoint."); err != nil {
		return fmt.Errorf("list backends: %w", err)
	}
	return nil
}

// offered is every capability a kind can be asked for, conditional ones
// included and marked - the table is read by a person deciding what to write
// in a manifest, and a capability that exists under a condition is not the
// same answer as one that does not exist.
func offered(k backendKind) []string {
	out := slices.Clone(k.Capabilities)
	for _, need := range slices.Sorted(maps.Keys(k.Conditional)) {
		out = append(out, need+"*")
	}
	return out
}
