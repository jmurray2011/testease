package ec2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/target"
)

type fakeCLI struct{ calls [][]string }

func (f *fakeCLI) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch args[0] + " " + args[1] {
	case "ec2 run-instances":
		return []byte(`{"Instances":[{"InstanceId":"i-fictional","ImageId":"ami-fictional"}]}`), nil
	case "ssm describe-instance-information":
		return []byte(`{"InstanceInformationList":[{"InstanceId":"i-fictional","PingStatus":"Online"}]}`), nil
	case "ec2 describe-instances":
		return []byte(`{"Reservations":[{"Instances":[{"InstanceId":"i-fictional","ImageId":"ami-fictional","State":{"Name":"running"},"Tags":[{"Key":"testease.run","Value":"run-fictional"},{"Key":"testease.runner","Value":"operator@workstation"}]}]}]}`), nil
	case "sts get-caller-identity":
		return []byte(`{"Account":"000000000000"}`), nil
	case "ec2 describe-images":
		return []byte(`{"Images":[{"ImageId":"ami-fictional"}]}`), nil
	case "ec2 describe-subnets":
		return []byte(`{"Subnets":[{"SubnetId":"subnet-fictional"}]}`), nil
	case "iam get-instance-profile":
		return []byte(`{"InstanceProfile":{"InstanceProfileName":"fictional-instance-profile"}}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func TestEC2DoctorChecksLaunchPrerequisites(t *testing.T) {
	t.Parallel()
	cli := &fakeCLI{}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1"}
	err := driver.Check(context.Background(), "ami-fictional", map[string]string{
		"ec2.instance_type": "t3.small", "ec2.subnet_id": "subnet-fictional",
		"ec2.instance_profile": "fictional-instance-profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls string
	for _, call := range cli.calls {
		calls += strings.Join(call, " ") + "\n"
	}
	for _, want := range []string{"sts get-caller-identity", "ec2 describe-images", "ec2 describe-subnets", "iam get-instance-profile"} {
		if !strings.Contains(calls, want) {
			t.Errorf("doctor calls lack %q: %s", want, calls)
		}
	}
}

func TestEC2DriverCreatesLabelsAndFindsItsInstance(t *testing.T) {
	t.Parallel()
	cli := &fakeCLI{}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1"}
	labels := map[string]string{target.LabelRun: "run-fictional", target.LabelRunner: "operator@workstation"}
	machine, err := driver.Create(context.Background(), provisioned.Request{
		Image: "ami-fictional", Labels: labels,
		Provision: map[string]string{
			"ec2.instance_type": "t3.small", "ec2.subnet_id": "subnet-fictional",
			"ec2.instance_profile": "fictional-instance-profile",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if machine.ID != "i-fictional" || machine.Address != "i-fictional" || machine.Labels[target.LabelRun] != "run-fictional" {
		t.Errorf("created machine = %+v", machine)
	}
	create := strings.Join(cli.calls[0], " ")
	for _, want := range []string{"--image-id ami-fictional", "--instance-type t3.small", "--subnet-id subnet-fictional", "--iam-instance-profile Name=fictional-instance-profile", "testease.run", "run-fictional"} {
		if !strings.Contains(create, want) {
			t.Errorf("create call %q lacks %q", create, want)
		}
	}
	if err := driver.WaitDialable(context.Background(), machine, time.Second); err != nil {
		t.Fatal(err)
	}
	found, err := driver.List(context.Background(), labels)
	if err != nil || len(found) != 1 || found[0].ID != machine.ID {
		t.Errorf("listed = %+v, %v", found, err)
	}
	if err := driver.Destroy(context.Background(), machine); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cli.calls[len(cli.calls)-1], " "); !strings.Contains(got, "terminate-instances --instance-ids i-fictional") {
		t.Errorf("destroy call = %q", got)
	}
}
