// Package ecs runs one-shot probes as Fargate tasks through the AWS CLI.
// The selected task definition is pre-registered and pinned to the requested
// ECR image; each probe is a separate, tagged task.
package ecs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/target"
)

type CLI interface {
	Output(context.Context, ...string) ([]byte, error)
}

type Driver struct {
	CLI     CLI
	Profile string
	Region  string
	Cluster string
}

var _ provisioned.OneShotDriver = Driver{}

func (d Driver) check() error {
	if d.CLI == nil || d.Profile == "" || d.Region == "" {
		return errors.New("ECS requires an explicit AWS profile and region")
	}
	return nil
}

// Check verifies the selected registry image, task definition, cluster, and
// network before RunTask could spend capacity.
func (d Driver) Check(ctx context.Context, image string, provision map[string]string) error {
	if err := d.check(); err != nil {
		return err
	}
	for _, key := range []string{"ecs.cluster", "ecs.task_definition", "ecs.subnet_id", "ecs.security_group_id"} {
		if _, err := required(provision, key); err != nil {
			return err
		}
	}
	registry, rest, ok := strings.Cut(image, "/")
	if !ok {
		return fmt.Errorf("ECS image %q is not a private ECR reference", image)
	}
	parts := strings.Split(registry, ".")
	if len(parts) < 6 || parts[1] != "dkr" || parts[2] != "ecr" || parts[3] != d.Region || parts[4] != "amazonaws" {
		return fmt.Errorf("ECS image %q is not in selected AWS region %s", image, d.Region)
	}
	account := parts[0]
	repository, imageID := "", ""
	if before, digest, found := strings.Cut(rest, "@"); found {
		repository, imageID = before, "imageDigest="+digest
	} else if index := strings.LastIndex(rest, ":"); index > 0 {
		repository, imageID = rest[:index], "imageTag="+rest[index+1:]
	}
	if repository == "" || strings.HasSuffix(imageID, "=") {
		return fmt.Errorf("ECS image %q needs a repository and tag or digest", image)
	}
	out, err := d.CLI.Output(ctx, "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return fmt.Errorf("resolve AWS profile %q: %w", d.Profile, err)
	}
	var caller struct{ Account string }
	if err := json.Unmarshal(out, &caller); err != nil || caller.Account != account {
		return fmt.Errorf("ECR registry account %s differs from selected profile's account", account)
	}
	out, err = d.CLI.Output(ctx, "ecr", "describe-images", "--registry-id", account,
		"--repository-name", repository, "--image-ids", imageID, "--output", "json")
	if err != nil {
		return fmt.Errorf("find ECR image %s: %w", image, err)
	}
	var images struct {
		Details []struct {
			RegistryID string   `json:"registryId"`
			Repository string   `json:"repositoryName"`
			Digest     string   `json:"imageDigest"`
			Tags       []string `json:"imageTags"`
		} `json:"imageDetails"`
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return err
	}
	found := false
	for _, item := range images.Details {
		if item.RegistryID != account || item.Repository != repository {
			continue
		}
		if strings.HasPrefix(imageID, "imageTag=") && slices.Contains(item.Tags, strings.TrimPrefix(imageID, "imageTag=")) ||
			strings.HasPrefix(imageID, "imageDigest=") && item.Digest == strings.TrimPrefix(imageID, "imageDigest=") {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("ECR image %s was not found", image)
	}
	cluster := provision["ecs.cluster"]
	out, err = d.CLI.Output(ctx, "ecs", "describe-clusters", "--clusters", cluster, "--output", "json")
	if err != nil {
		return fmt.Errorf("find ECS cluster %s: %w", cluster, err)
	}
	var clusters struct {
		Clusters []struct {
			Name   string `json:"clusterName"`
			Status string `json:"status"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal(out, &clusters); err != nil {
		return err
	}
	if len(clusters.Clusters) != 1 || clusters.Clusters[0].Status != "ACTIVE" {
		return fmt.Errorf("ECS cluster %s is not active", cluster)
	}
	if _, err := d.describeDefinition(ctx, image, provision); err != nil {
		return err
	}
	for _, item := range []struct{ service, action, flag, key, result, idKey string }{
		{"ec2", "describe-subnets", "--subnet-ids", "ecs.subnet_id", "Subnets", "SubnetId"},
		{"ec2", "describe-security-groups", "--group-ids", "ecs.security_group_id", "SecurityGroups", "GroupId"},
	} {
		out, err := d.CLI.Output(ctx, item.service, item.action, item.flag, provision[item.key], "--output", "json")
		if err != nil {
			return fmt.Errorf("find %s %s: %w", item.key, provision[item.key], err)
		}
		var response map[string][]map[string]any
		if err := json.Unmarshal(out, &response); err != nil {
			return err
		}
		if len(response[item.result]) != 1 || response[item.result][0][item.idKey] != provision[item.key] {
			return fmt.Errorf("%s %s was not found", item.key, provision[item.key])
		}
	}
	return nil
}

func required(provision map[string]string, key string) (string, error) {
	value := provision[key]
	if value == "" {
		return "", fmt.Errorf("ECS requires catalog provision setting %q", key)
	}
	return value, nil
}

type definition struct {
	TaskDefinition struct {
		ARN             string   `json:"taskDefinitionArn"`
		NetworkMode     string   `json:"networkMode"`
		Compatibilities []string `json:"requiresCompatibilities"`
		Containers      []struct {
			Name       string   `json:"name"`
			Image      string   `json:"image"`
			EntryPoint []string `json:"entryPoint"`
			Log        struct {
				Driver  string            `json:"logDriver"`
				Options map[string]string `json:"options"`
			} `json:"logConfiguration"`
		} `json:"containerDefinitions"`
	} `json:"taskDefinition"`
}

func (d Driver) describeDefinition(ctx context.Context, image string, provision map[string]string) (definition, error) {
	name, err := required(provision, "ecs.task_definition")
	if err != nil {
		return definition{}, err
	}
	out, err := d.CLI.Output(ctx, "ecs", "describe-task-definition", "--task-definition", name, "--output", "json")
	if err != nil {
		return definition{}, fmt.Errorf("describe ECS task definition %s: %w", name, err)
	}
	var def definition
	if err := json.Unmarshal(out, &def); err != nil {
		return definition{}, err
	}
	if def.TaskDefinition.ARN == "" || def.TaskDefinition.NetworkMode != "awsvpc" || !slices.Contains(def.TaskDefinition.Compatibilities, "FARGATE") {
		return definition{}, fmt.Errorf("task definition %s is not Fargate-compatible awsvpc", name)
	}
	if len(def.TaskDefinition.Containers) != 1 {
		return definition{}, fmt.Errorf("task definition %s must have exactly one probe container", name)
	}
	c := def.TaskDefinition.Containers[0]
	if c.Image != image {
		return definition{}, fmt.Errorf("task definition %s uses image %q, want %q", name, c.Image, image)
	}
	if !slices.Equal(c.EntryPoint, []string{"/bin/sh", "-c"}) {
		return definition{}, fmt.Errorf("task definition %s container must enter through /bin/sh -c", name)
	}
	if c.Log.Driver != "awslogs" || c.Log.Options["awslogs-group"] == "" || c.Log.Options["awslogs-stream-prefix"] == "" {
		return definition{}, fmt.Errorf("task definition %s needs awslogs group and stream prefix", name)
	}
	return def, nil
}

func (d Driver) Create(ctx context.Context, req provisioned.Request) (provisioned.Machine, error) {
	if err := d.check(); err != nil {
		return provisioned.Machine{}, err
	}
	for _, key := range []string{"ecs.cluster", "ecs.task_definition", "ecs.subnet_id", "ecs.security_group_id"} {
		if _, err := required(req.Provision, key); err != nil {
			return provisioned.Machine{}, err
		}
	}
	def, err := d.describeDefinition(ctx, req.Image, req.Provision)
	if err != nil {
		return provisioned.Machine{}, err
	}
	c := def.TaskDefinition.Containers[0]
	return provisioned.Machine{
		ID: def.TaskDefinition.ARN, Address: def.TaskDefinition.ARN, Image: req.Image,
		Labels: maps.Clone(req.Labels),
		Resolved: map[string]any{
			"task_definition_arn": def.TaskDefinition.ARN, "image": req.Image,
			"cluster": req.Provision["ecs.cluster"], "subnet_id": req.Provision["ecs.subnet_id"],
			"security_group_id": req.Provision["ecs.security_group_id"],
			"assign_public_ip":  req.Provision["ecs.assign_public_ip"],
			"container":         c.Name, "log_group": c.Log.Options["awslogs-group"],
			"log_prefix":      c.Log.Options["awslogs-stream-prefix"],
			"account_profile": d.Profile, "region": d.Region,
		},
	}, nil
}

func (d Driver) WaitDialable(context.Context, provisioned.Machine, time.Duration) error { return nil }

func field(m provisioned.Machine, key string) string {
	value, _ := m.Resolved[key].(string)
	return value
}

func (d Driver) RunOnce(ctx context.Context, parent provisioned.Machine, script []byte, args []string,
	opt target.RunOpts, created func(provisioned.Machine) error,
) (target.Result, error) {
	if err := d.check(); err != nil {
		return target.Result{RC: -1}, err
	}
	start := time.Now()
	assignPublic := "DISABLED"
	if strings.EqualFold(field(parent, "assign_public_ip"), "enabled") {
		assignPublic = "ENABLED"
	}
	env := make([]map[string]string, 0, len(opt.Env))
	for _, entry := range opt.Env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name != "" {
			env = append(env, map[string]string{"name": name, "value": value})
		}
	}
	payload := map[string]any{
		"cluster": field(parent, "cluster"), "taskDefinition": parent.ID,
		"launchType": "FARGATE", "count": 1,
		"networkConfiguration": map[string]any{"awsvpcConfiguration": map[string]any{
			"subnets":        []string{field(parent, "subnet_id")},
			"securityGroups": []string{field(parent, "security_group_id")},
			"assignPublicIp": assignPublic,
		}},
		"overrides": map[string]any{"containerOverrides": []any{map[string]any{
			"name": field(parent, "container"), "command": []string{wrapper(script, args, opt.Fetch)},
			"environment": env,
		}}},
		"tags": tags(parent.Labels),
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return target.Result{RC: -1}, err
	}
	out, err := d.CLI.Output(ctx, "ecs", "run-task", "--cli-input-json", string(input), "--output", "json")
	if err != nil {
		return target.Result{RC: -1}, fmt.Errorf("run Fargate task: %w", err)
	}
	var started struct {
		Tasks []struct {
			ARN string `json:"taskArn"`
		} `json:"tasks"`
		Failures []struct {
			Reason string `json:"reason"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(out, &started); err != nil {
		return target.Result{RC: -1}, err
	}
	if len(started.Tasks) != 1 || started.Tasks[0].ARN == "" || len(started.Failures) != 0 {
		return target.Result{RC: -1}, fmt.Errorf("RunTask returned %d tasks and %d failures", len(started.Tasks), len(started.Failures))
	}
	task := provisioned.Machine{
		ID: started.Tasks[0].ARN, Address: started.Tasks[0].ARN,
		Image: parent.Image, Labels: maps.Clone(parent.Labels), Resolved: map[string]any{
			"task_arn": started.Tasks[0].ARN, "task_definition_arn": parent.ID,
			"cluster": field(parent, "cluster"), "image": parent.Image,
		},
	}
	if err := created(task); err != nil {
		_ = d.Destroy(context.WithoutCancel(ctx), task)
		return target.Result{RC: -1}, fmt.Errorf("record created Fargate task: %w", err)
	}
	budget := opt.Timeout
	if budget == 0 {
		budget = 45 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	exitCode, err := d.waitStopped(waitCtx, task, field(parent, "container"))
	if err != nil {
		_ = d.Destroy(context.WithoutCancel(ctx), task)
		return target.Result{RC: -1, TimedOut: errors.Is(waitCtx.Err(), context.DeadlineExceeded), Duration: time.Since(start)}, err
	}
	result := target.Result{RC: exitCode, Duration: time.Since(start), Fetched: map[string][]byte{}}
	stream := field(parent, "log_prefix") + "/" + field(parent, "container") + "/" + path.Base(task.ID)
	if err := d.collectLogs(ctx, field(parent, "log_group"), stream, opt, &result); err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, "Fargate awslogs merges stdout and stderr into one stream")
	return result, nil
}

func tags(labels map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(labels))
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		out = append(out, map[string]string{"key": key, "value": labels[key]})
	}
	return out
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func wrapper(script []byte, args, fetch []string) string {
	var b strings.Builder
	b.WriteString("set +e\nprobe=/tmp/testease-probe-$$\nfacts=/tmp/testease-facts-$$\n")
	b.WriteString("printf '%s' ")
	b.WriteString(shellQuote(base64.StdEncoding.EncodeToString(script)))
	b.WriteString(" | base64 -d > \"$probe\"\nchmod 700 \"$probe\"\nexport TESTEASE_FACTS=\"$facts\"\n/bin/sh \"$probe\"")
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(arg))
	}
	b.WriteString(" 3>>\"$facts\"")
	b.WriteString("\nrc=$?\nif [ -f \"$facts\" ]; then printf '@@TESTEASE_FACTS:%s\\n' \"$(base64 \"$facts\" | tr -d '\\n')\"; fi\n")
	for i, item := range fetch {
		fmt.Fprintf(&b, "if [ -f %s ]; then base64 %s | tr -d '\\n' | fold -w 32000 | while IFS= read -r chunk || [ -n \"$chunk\" ]; do printf '@@TESTEASE_ARTIFACT:%d:%%s\\n' \"$chunk\"; done; fi\n",
			shellQuote(item), shellQuote(item), i)
	}
	b.WriteString("echo '@@TESTEASE_DONE'\nexit \"$rc\"\n")
	return b.String()
}

func (d Driver) waitStopped(ctx context.Context, task provisioned.Machine, container string) (int, error) {
	for {
		out, err := d.CLI.Output(ctx, "ecs", "describe-tasks", "--cluster", field(task, "cluster"),
			"--tasks", task.ID, "--output", "json")
		if err != nil {
			return -1, fmt.Errorf("describe Fargate task %s: %w", task.ID, err)
		}
		var response struct {
			Tasks []struct {
				ARN        string `json:"taskArn"`
				Status     string `json:"lastStatus"`
				Containers []struct {
					Name     string `json:"name"`
					ExitCode *int   `json:"exitCode"`
				} `json:"containers"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(out, &response); err != nil {
			return -1, err
		}
		for _, item := range response.Tasks {
			if item.ARN != task.ID || item.Status != "STOPPED" {
				continue
			}
			for _, c := range item.Containers {
				if c.Name == container && c.ExitCode != nil {
					return *c.ExitCode, nil
				}
			}
			return -1, fmt.Errorf("fargate task %s stopped without probe exit code", task.ID)
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (d Driver) collectLogs(ctx context.Context, group, stream string, opt target.RunOpts, result *target.Result) error {
	logsCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		out, err := d.CLI.Output(logsCtx, "logs", "get-log-events", "--log-group-name", group,
			"--log-stream-name", stream, "--start-from-head", "--output", "json")
		if err == nil {
			var response struct {
				Events []struct {
					Message string `json:"message"`
				} `json:"events"`
			}
			if err := json.Unmarshal(out, &response); err != nil {
				return err
			}
			var stdout bytes.Buffer
			var facts bytes.Buffer
			chunks := map[int]string{}
			done := false
			for _, event := range response.Events {
				message := event.Message
				switch {
				case message == "@@TESTEASE_DONE":
					done = true
				case strings.HasPrefix(message, "@@TESTEASE_FACTS:"):
					body, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(message, "@@TESTEASE_FACTS:"))
					if decodeErr != nil {
						return fmt.Errorf("decode task facts: %w", decodeErr)
					}
					_, _ = facts.Write(body)
				case strings.HasPrefix(message, "@@TESTEASE_ARTIFACT:"):
					parts := strings.SplitN(strings.TrimPrefix(message, "@@TESTEASE_ARTIFACT:"), ":", 2)
					if len(parts) != 2 {
						return fmt.Errorf("malformed task artifact marker")
					}
					index, parseErr := strconv.Atoi(parts[0])
					if parseErr != nil || index < 0 || index >= len(opt.Fetch) {
						return fmt.Errorf("invalid task artifact index %q", parts[0])
					}
					chunks[index] += parts[1]
				default:
					stdout.WriteString(message)
					stdout.WriteByte('\n')
				}
			}
			if done {
				for index, encoded := range chunks {
					body, decodeErr := base64.StdEncoding.DecodeString(encoded)
					if decodeErr != nil {
						return fmt.Errorf("decode task artifact %s: %w", opt.Fetch[index], decodeErr)
					}
					result.Fetched[opt.Fetch[index]] = body
				}
				if opt.Stdout != nil {
					_, _ = io.Copy(opt.Stdout, &stdout)
				}
				if opt.Structured != nil {
					_, _ = io.Copy(opt.Structured, &facts)
				}
				return nil
			}
		}
		select {
		case <-logsCtx.Done():
			return fmt.Errorf("fargate logs for %s did not reach completion marker: %w", stream, logsCtx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

func (d Driver) Destroy(ctx context.Context, machine provisioned.Machine) error {
	if strings.Contains(machine.ID, ":task-definition/") {
		return nil
	}
	cluster := field(machine, "cluster")
	if cluster == "" {
		cluster = d.Cluster
	}
	if cluster == "" {
		return errors.New("ECS stop needs a cluster")
	}
	_, err := d.CLI.Output(ctx, "ecs", "stop-task", "--cluster", cluster, "--task", machine.ID, "--reason", "testease reap", "--output", "json")
	if err != nil && !strings.Contains(err.Error(), "TaskNotFoundException") {
		return err
	}
	return nil
}

func (d Driver) List(ctx context.Context, labels map[string]string) ([]provisioned.Machine, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if d.Cluster == "" || labels[target.LabelRunner] == "" {
		return nil, errors.New("ECS list requires a cluster and testease.runner label")
	}
	out, err := d.CLI.Output(ctx, "ecs", "list-tasks", "--cluster", d.Cluster, "--desired-status", "RUNNING", "--output", "json")
	if err != nil {
		return nil, err
	}
	var listed struct {
		ARNs []string `json:"taskArns"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, err
	}
	if len(listed.ARNs) == 0 {
		return nil, nil
	}
	var machines []provisioned.Machine
	for start := 0; start < len(listed.ARNs); start += 100 {
		end := min(start+100, len(listed.ARNs))
		args := append([]string{"ecs", "describe-tasks", "--cluster", d.Cluster, "--tasks"}, listed.ARNs[start:end]...)
		args = append(args, "--include", "TAGS", "--output", "json")
		out, err = d.CLI.Output(ctx, args...)
		if err != nil {
			return nil, err
		}
		var response struct {
			Tasks []struct {
				ARN            string                        `json:"taskArn"`
				TaskDefinition string                        `json:"taskDefinitionArn"`
				Tags           []struct{ Key, Value string } `json:"tags"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(out, &response); err != nil {
			return nil, err
		}
		for _, task := range response.Tasks {
			tags := map[string]string{}
			for _, tag := range task.Tags {
				tags[tag.Key] = tag.Value
			}
			matches := true
			for key, value := range labels {
				if tags[key] != value {
					matches = false
					break
				}
			}
			if matches && task.ARN != "" {
				machines = append(machines, provisioned.Machine{
					ID: task.ARN, Address: task.ARN,
					Labels: tags, Resolved: map[string]any{"cluster": d.Cluster, "task_definition_arn": task.TaskDefinition},
				})
			}
		}
	}
	return machines, nil
}
