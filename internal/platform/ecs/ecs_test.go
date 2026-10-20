package ecs

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/target"
)

const fictionalImage = "000000000000.dkr.ecr.us-east-1.amazonaws.com/fictional/probe:1"
const fictionalTask = "arn:aws:ecs:us-east-1:000000000000:task/fictional-cluster/task-fictional"
const fictionalDefinition = "arn:aws:ecs:us-east-1:000000000000:task-definition/fictional:1"

type fakeCLI struct{ calls [][]string }
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
		return []byte(`{"events":[{"message":"human log"},{"message":"@@TESTEASE_FACTS:ZmFjdAlrZXkJdmFsdWUK"},{"message":"@@TESTEASE_ARTIFACT:0:Ynl0ZXM="},{"message":"@@TESTEASE_DONE"}]}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func TestFargateRunsOneTaskAndCapturesItsEvidence(t *testing.T) {
	t.Parallel()
	cli := &fakeCLI{}
	driver := Driver{CLI: cli, Profile: "fictional-profile", Region: "us-east-1"}
	machine, err := driver.Create(context.Background(), provisioned.Request{
		Image: fictionalImage,
		Provision: map[string]string{"ecs.cluster": "fictional-cluster", "ecs.task_definition": fictionalDefinition,
			"ecs.subnet_id": "subnet-fictional", "ecs.security_group_id": "sg-fictional"},
		Labels: map[string]string{target.LabelRun: "run-fictional", target.LabelRunner: "operator@workstation"},
	})
	if err != nil { t.Fatal(err) }
	if machine.ID != fictionalDefinition { t.Errorf("resolved task definition = %+v", machine) }
	var stdout, structured bytes.Buffer
	var created string
	result, err := driver.RunOnce(context.Background(), machine, []byte("exit 7\n"), nil,
		target.RunOpts{Stdout: &stdout, Structured: &structured, Fetch: []string{"/tmp/result.bin"}},
		func(task provisioned.Machine) error { created = task.ID; return nil })
	if err != nil { t.Fatal(err) }
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
				if !strings.Contains(joined, want) { t.Errorf("run-task lacks %q: %s", want, joined) }
			}
			return
		}
	}
	t.Fatal("RunTask was never called")
}
