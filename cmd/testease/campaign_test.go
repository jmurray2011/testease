package main

import (
	"reflect"
	"testing"

	"github.com/jmurray2011/testease/internal/campaign"
)

func TestCampaignScopeOverridesCanPinOrSweepDefinitionValues(t *testing.T) {
	t.Parallel()

	cmd := campaignRunCmd()
	for name, value := range map[string]string{
		"param":  "from=7.1.3",
		"matrix": "edition=writer,docs",
		"par":    "1",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	definition := &campaign.Definition{
		Params:   map[string]string{"edition": "docs", "to": "7.2.3"},
		Matrix:   map[string][]string{"from": {"6.2.4", "7.2.1"}},
		Parallel: 4,
	}

	if err := applyCampaignScopeOverrides(cmd, definition); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"from": "7.1.3", "to": "7.2.3"}; !reflect.DeepEqual(definition.Params, want) {
		t.Fatalf("params = %#v, want %#v", definition.Params, want)
	}
	if want := map[string][]string{"edition": {"writer", "docs"}}; !reflect.DeepEqual(definition.Matrix, want) {
		t.Fatalf("matrix = %#v, want %#v", definition.Matrix, want)
	}
	if definition.Parallel != 1 {
		t.Fatalf("parallel = %d, want 1", definition.Parallel)
	}
}

func TestCampaignScopeOverridesInitializeEmptyMaps(t *testing.T) {
	t.Parallel()

	cmd := campaignRunCmd()
	if err := cmd.Flags().Set("param", "to=7.2.3"); err != nil {
		t.Fatal(err)
	}
	definition := &campaign.Definition{Parallel: 1}
	if err := applyCampaignScopeOverrides(cmd, definition); err != nil {
		t.Fatal(err)
	}
	if definition.Params["to"] != "7.2.3" || definition.Matrix == nil {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestCampaignScopeOverrideRefusesTheSamePinnedAndSweptParameter(t *testing.T) {
	t.Parallel()

	cmd := campaignRunCmd()
	if err := cmd.Flags().Set("param", "from=7.1.3"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("matrix", "from=6.2.4,7.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := applyCampaignScopeOverrides(cmd, &campaign.Definition{Parallel: 1}); err == nil {
		t.Fatal("override pinned and swept the same parameter")
	}
}
