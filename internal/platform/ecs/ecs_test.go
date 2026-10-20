package ecs

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/target"
)

func TestFargateArtifactLogFramesStayBounded(t *testing.T) {
	t.Parallel()
	artifact := filepath.Join(t.TempDir(), "large.bin")
	command := wrapper([]byte(fmt.Sprintf("head -c 70000 /dev/zero > %s\n", shellQuote(artifact))), nil, []string{artifact})
	out, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		t.Fatal(err)
	}
	var encoded string
	frames := 0
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "@@TESTEASE_ARTIFACT:0:") {
			continue
		}
		frames++
		if len(line) > 33000 {
			t.Errorf("artifact log frame is %d bytes", len(line))
		}
		encoded += strings.TrimPrefix(line, "@@TESTEASE_ARTIFACT:0:")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if frames < 2 || len(decoded) != 70000 {
		t.Errorf("frames=%d decoded bytes=%d", frames, len(decoded))
	}
}

const (
	fictionalImage      = "000000000000.dkr.ecr.us-east-1.amazonaws.com/fictional/probe:1"
	fictionalTask       = "arn:aws:ecs:us-east-1:000000000000:task/fictional-cluster/task-fictional"
	fictionalDefinition = "arn:aws:ecs:us-east-1:000000000000:task-definition/fictional:1"
)

type fakeCLI struct {
	calls       [][]string
	partialLogs bool
	logCalls    int
}

func (f *fakeCLI) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch args[0] + " " + args[1] {
	case "ecs describe-task-definition":
		return []byte(`{"taskDefinition":{"taskDefinitionArn":"` + fictionalDefinition + `","networkMode":"awsvpc","requiresCompatibilities":["FARGATE"],"containerDefinitions":[{"name":"probe","image":"` + fictionalImage + `","entryPoint":["/bin/sh","-c"],"logConfiguration":{"logDriver":"awslogs","options":{"awslogs-group":"/fictional/logs","awslogs-stream-prefix":"testease"}}}]}}`), nil
	case "ecs run-task":
		return []byte(`{"tasks":[{"taskArn":"` + fictionalTask + `"}],"failures":[]}`), nil
	case "ecs describe-tasks":
		return []byte(`{"tasks":[{"taskArn":"` + fictionalTask + `","lastStatus":"STOPPED","containers":[{"name":"probe","exitCode":7}]}],"failures":[]}`), nil
	case "logs get-log-events":
		f.logCalls++
		if f.partialLogs && f.logCalls == 1 {
			return []byte(`{"events":[{"message":"human log"},{"message":"@@TESTEASE_FACTS:ZmFjdAlrZXkJdmFsdWUK"}]}`), nil
		}
		return []byte(`{"events":[{"message":"human log"},{"message":"@@TESTEASE_FACTS:ZmFjdAlrZXkJdmFsdWUK"},{"message":"@@TESTEASE_ARTIFACT:0:Ynl0ZXM="},{"message":"@@TESTEASE_DONE"}]}`), nil
	case "sts get-caller-identity":
		return []byte(`{"Account":"000000000000"}`), nil
	case "ecr describe-images":
		return []byte(`{"imageDetails":[{"registryId":"000000000000","repositoryName":"fictional/probe","imageTags":["1"]}]}`), nil
	case "ecs describe-clusters":
		return []byte(`{"clusters":[{"clusterName":"fictional-cluster","status":"ACTIVE"}],"failures":[]}`), nil
	case "ec2 describe-subnets":
		return []byte(`{"Subnets":[{"SubnetId":"subnet-fictional"}]}`), nil
	case "ec2 describe-security-groups":
		return []byte(`{"SecurityGroups":[{"GroupId":"sg-fictional"}]}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func TestFargateFactsAreEmittedOnceWhenLogsArriveLate(t *testing.T) {
	t.Parallel()
	cli := &fakeCLI{partialLogs: true}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1"}
	machine, err := driver.Create(context.Background(), provisioned.Request{
		Image: fictionalImage,
		Provision: map[string]string{
			"ecs.cluster": "fictional-cluster", "ecs.task_definition": fictionalDefinition,
			"ecs.subnet_id": "subnet-fictional", "ecs.security_group_id": "sg-fictional",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var structured bytes.Buffer
	_, err = driver.RunOnce(context.Background(), machine, []byte("true\n"), nil,
		target.RunOpts{Structured: &structured, Fetch: []string{"/tmp/result.bin"}}, func(provisioned.Machine) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if structured.String() != "fact\tkey\tvalue\n" {
		t.Errorf("facts duplicated during log poll: %q", structured.String())
	}
}

func TestFargateDoctorChecksECRAndLaunchInputs(t *testing.T) {
	t.Parallel()
	cli := &fakeCLI{}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1"}
	err := driver.Check(context.Background(), fictionalImage, map[string]string{
		"ecs.cluster": "fictional-cluster", "ecs.task_definition": fictionalDefinition,
		"ecs.subnet_id": "subnet-fictional", "ecs.security_group_id": "sg-fictional",
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls string
	for _, call := range cli.calls {
		calls += strings.Join(call, " ") + "\n"
	}
	for _, want := range []string{"sts get-caller-identity", "ecr describe-images", "ecs describe-clusters", "ecs describe-task-definition", "ec2 describe-subnets", "ec2 describe-security-groups"} {
		if !strings.Contains(calls, want) {
			t.Errorf("doctor did not check %s: %s", want, calls)
		}
	}
}

func TestFargateRunsOneTaskAndCapturesItsEvidence(t *testing.T) {
	t.Parallel()
	cli := &fakeCLI{}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1"}
	machine, err := driver.Create(context.Background(), provisioned.Request{
		Image: fictionalImage,
		Provision: map[string]string{
			"ecs.cluster": "fictional-cluster", "ecs.task_definition": fictionalDefinition,
			"ecs.subnet_id": "subnet-fictional", "ecs.security_group_id": "sg-fictional",
		},
		Labels: map[string]string{target.LabelRun: "run-fictional", target.LabelRunner: "operator@workstation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if machine.ID != fictionalDefinition {
		t.Errorf("resolved task definition = %+v", machine)
	}
	var stdout, structured bytes.Buffer
	var created string
	result, err := driver.RunOnce(context.Background(), machine, []byte("exit 7\n"), nil,
		target.RunOpts{Stdout: &stdout, Structured: &structured, Fetch: []string{"/tmp/result.bin"}},
		func(task provisioned.Machine) error { created = task.ID; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if created != fictionalTask || result.RC != 7 || !strings.Contains(stdout.String(), "human log") {
		t.Errorf("task=%q result=%+v stdout=%q", created, result, stdout.String())
	}
	if structured.String() != "fact\tkey\tvalue\n" || string(result.Fetched["/tmp/result.bin"]) != "bytes" {
		t.Errorf("structured=%q fetched=%v", structured.String(), result.Fetched)
	}
	for _, call := range cli.calls {
		if len(call) >= 2 && call[0] == "ecs" && call[1] == "run-task" {
			joined := strings.Join(call, " ")
			for _, want := range []string{"--cli-input-json", fictionalDefinition, "FARGATE", "subnet-fictional", "testease.run"} {
				if !strings.Contains(joined, want) {
					t.Errorf("run-task lacks %q: %s", want, joined)
				}
			}
			return
		}
	}
	t.Fatal("RunTask was never called")
}

type manyTasksCLI struct{ batches []int }

func (f *manyTasksCLI) Output(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] + " " + args[1] {
	case "ecs list-tasks":
		var body strings.Builder
		body.WriteString(`{"taskArns":[`)
		for i := range 101 {
			if i > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `"task-%d"`, i)
		}
		body.WriteString(`]}`)
		return []byte(body.String()), nil
	case "ecs describe-tasks":
		start := 0
		for i, arg := range args {
			if arg == "--tasks" {
				start = i + 1
				break
			}
		}
		end := start
		for end < len(args) && args[end] != "--include" {
			end++
		}
		f.batches = append(f.batches, end-start)
		if end-start > 100 {
			return nil, fmt.Errorf("DescribeTasks accepts at most 100 task IDs")
		}
		var body strings.Builder
		body.WriteString(`{"tasks":[`)
		for i, arn := range args[start:end] {
			if i > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `{"taskArn":%q,"tags":[{"key":"testease.runner","value":"operator@workstation"}]}`, arn)
		}
		body.WriteString(`]}`)
		return []byte(body.String()), nil
	}
	return nil, fmt.Errorf("unexpected CLI command: %v", args)
}

func TestFargateListBatchesTaskDescriptions(t *testing.T) {
	t.Parallel()
	cli := &manyTasksCLI{}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1", Cluster: "fictional-cluster"}
	machines, err := driver.List(context.Background(), map[string]string{target.LabelRunner: "operator@workstation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 101 || len(cli.batches) != 2 || cli.batches[0] != 100 || cli.batches[1] != 1 {
		t.Errorf("machines=%d describe batches=%v", len(machines), cli.batches)
	}
}
