package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudProvidersRegistersEC2(t *testing.T) {
	t.Parallel()
	providers := cloudProviders(awsAccountBinding{Profile: "fictional-profile", Region: "us-east-1"},
		"run-fictional", "operator@workstation", "", slog.Default())
	if providers["ec2"] == nil {
		t.Fatal("EC2 was not registered as a target provider")
	}
}

func TestAWSBindingUsesCatalogAndRefusesAccountDisagreement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte(`schema: 1
targets:
  - name: fictional-cloud
    params: {provider: ec2, image: ami-fictional}
    provision: {aws.profile: fictional-profile, aws.region: us-east-1, ec2.instance_type: t3.small}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := runCmd()
	if err := cmd.ParseFlags([]string{"--target", "fictional-cloud", "--target-catalog", path}); err != nil {
		t.Fatal(err)
	}
	params, selection, err := selectedParams(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := params["ec2.instance_type"]; ok {
		t.Fatal("provision setting entered scenario params")
	}
	binding, err := awsBinding(cmd, selection)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Profile != "fictional-profile" || binding.Region != "us-east-1" {
		t.Errorf("binding = %+v", binding)
	}
	if err := cmd.Flags().Set("aws-profile", "wrong-account"); err != nil {
		t.Fatal(err)
	}
	if _, err := awsBinding(cmd, selection); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Errorf("conflicting binding error = %v", err)
	}
}
