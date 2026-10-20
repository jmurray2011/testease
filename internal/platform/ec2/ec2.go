// Package ec2 provisions EC2 instances through the AWS CLI.
package ec2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/target"
)

// CLI is the small AWS command surface used by the driver.
type CLI interface {
	Output(context.Context, ...string) ([]byte, error)
}

type Driver struct {
	CLI     CLI
	Profile string
	Region  string
}

var _ provisioned.Driver = Driver{}

func (d Driver) check() error {
	if d.CLI == nil || d.Profile == "" || d.Region == "" {
		return errors.New("EC2 requires an explicit AWS profile and region")
	}
	return nil
}

// Check verifies the account and launch inputs without creating a resource.
func (d Driver) Check(ctx context.Context, image string, provision map[string]string) error {
	if err := d.check(); err != nil {
		return err
	}
	if image == "" {
		return errors.New("EC2 host needs an AMI id")
	}
	for _, key := range []string{"ec2.instance_type", "ec2.subnet_id", "ec2.instance_profile"} {
		if provision[key] == "" {
			return fmt.Errorf("EC2 requires catalog provision setting %q", key)
		}
	}
	out, err := d.CLI.Output(ctx, "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return fmt.Errorf("resolve AWS profile %q: %w", d.Profile, err)
	}
	var caller struct{ Account string }
	if err := json.Unmarshal(out, &caller); err != nil || caller.Account == "" {
		return fmt.Errorf("AWS profile %q returned no account identity", d.Profile)
	}
	out, err = d.CLI.Output(ctx, "ec2", "describe-images", "--image-ids", image, "--output", "json")
	if err != nil {
		return fmt.Errorf("find AMI %s in %s: %w", image, d.Region, err)
	}
	var images struct {
		Images []struct {
			ImageID string `json:"ImageId"`
		}
	}
	if err := json.Unmarshal(out, &images); err != nil || len(images.Images) != 1 || images.Images[0].ImageID != image {
		return fmt.Errorf("AMI %s was not found in %s", image, d.Region)
	}
	out, err = d.CLI.Output(ctx, "ec2", "describe-subnets", "--subnet-ids", provision["ec2.subnet_id"], "--output", "json")
	if err != nil {
		return fmt.Errorf("find subnet %s: %w", provision["ec2.subnet_id"], err)
	}
	var subnets struct {
		Subnets []struct {
			SubnetID string `json:"SubnetId"`
		}
	}
	if err := json.Unmarshal(out, &subnets); err != nil || len(subnets.Subnets) != 1 || subnets.Subnets[0].SubnetID != provision["ec2.subnet_id"] {
		return fmt.Errorf("subnet %s was not found in %s", provision["ec2.subnet_id"], d.Region)
	}
	out, err = d.CLI.Output(ctx, "iam", "get-instance-profile", "--instance-profile-name", provision["ec2.instance_profile"], "--output", "json")
	if err != nil {
		return fmt.Errorf("find instance profile %s: %w", provision["ec2.instance_profile"], err)
	}
	var profile struct {
		InstanceProfile struct {
			Name string `json:"InstanceProfileName"`
		} `json:"InstanceProfile"`
	}
	if err := json.Unmarshal(out, &profile); err != nil || profile.InstanceProfile.Name != provision["ec2.instance_profile"] {
		return fmt.Errorf("instance profile %s was not found", provision["ec2.instance_profile"])
	}
	return nil
}

func (d Driver) Create(ctx context.Context, req provisioned.Request) (provisioned.Machine, error) {
	if err := d.check(); err != nil {
		return provisioned.Machine{}, err
	}
	if req.Image == "" {
		return provisioned.Machine{}, errors.New("EC2 requires an AMI id")
	}
	for _, key := range []string{"ec2.instance_type", "ec2.subnet_id", "ec2.instance_profile"} {
		if req.Provision[key] == "" {
			return provisioned.Machine{}, fmt.Errorf("EC2 requires catalog provision setting %q", key)
		}
	}
	type tag struct{ Key, Value string }
	tags := make([]tag, 0, len(req.Labels))
	for _, key := range slices.Sorted(maps.Keys(req.Labels)) {
		tags = append(tags, tag{Key: key, Value: req.Labels[key]})
	}
	tagSpec, err := json.Marshal([]struct {
		ResourceType string
		Tags         []tag
	}{{ResourceType: "instance", Tags: tags}})
	if err != nil {
		return provisioned.Machine{}, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return provisioned.Machine{}, fmt.Errorf("create EC2 client token: %w", err)
	}
	args := []string{
		"ec2", "run-instances", "--image-id", req.Image,
		"--client-token", hex.EncodeToString(token[:]),
		"--instance-type", req.Provision["ec2.instance_type"],
		"--subnet-id", req.Provision["ec2.subnet_id"],
		"--iam-instance-profile", "Name=" + req.Provision["ec2.instance_profile"],
		"--count", "1",
		"--tag-specifications", string(tagSpec), "--output", "json",
	}
	if group := req.Provision["ec2.security_group_id"]; group != "" {
		args = append(args, "--security-group-ids", group)
	}
	var response struct {
		Instances []struct {
			InstanceID string `json:"InstanceId"`
			ImageID    string `json:"ImageId"`
		} `json:"Instances"`
	}
	var launchErr error
	for attempt := range 2 {
		callCtx := ctx
		if attempt > 0 {
			// The first request may have reached AWS even when its response did
			// not reach us. Retry with the same token and a bounded fresh context.
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
		}
		out, err := d.CLI.Output(callCtx, args...)
		if err == nil {
			err = json.Unmarshal(out, &response)
			if err == nil && len(response.Instances) == 1 && response.Instances[0].InstanceID != "" {
				launchErr = nil
				break
			}
			if err == nil {
				err = fmt.Errorf("EC2 create returned %d instances without one usable id", len(response.Instances))
			}
		}
		launchErr = err
	}
	if launchErr != nil {
		return provisioned.Machine{}, fmt.Errorf("create EC2 instance with client token %s: %w", hex.EncodeToString(token[:]), launchErr)
	}
	id := response.Instances[0].InstanceID
	return provisioned.Machine{
		ID: id, Address: id, Image: response.Instances[0].ImageID,
		Resolved: map[string]any{
			"ami_id": response.Instances[0].ImageID, "instance_id": id,
			"account_profile": d.Profile, "region": d.Region,
		},
		Labels: maps.Clone(req.Labels),
	}, nil
}

func (d Driver) WaitDialable(ctx context.Context, m provisioned.Machine, budget time.Duration) error {
	if err := d.check(); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		out, err := d.CLI.Output(waitCtx, "ssm", "describe-instance-information",
			"--filters", "Key=InstanceIds,Values="+m.ID, "--output", "json")
		if err != nil {
			return fmt.Errorf("check SSM registration for %s: %w", m.ID, err)
		}
		var response struct {
			Instances []struct {
				InstanceID string `json:"InstanceId"`
				PingStatus string `json:"PingStatus"`
			} `json:"InstanceInformationList"`
		}
		if err := json.Unmarshal(out, &response); err != nil {
			return fmt.Errorf("read SSM registration for %s: %w", m.ID, err)
		}
		for _, instance := range response.Instances {
			if instance.InstanceID == m.ID && instance.PingStatus == "Online" {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("instance %s did not become SSM dialable within %s: %w", m.ID, budget, waitCtx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

func (d Driver) List(ctx context.Context, labels map[string]string) ([]provisioned.Machine, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if labels[target.LabelRunner] == "" {
		return nil, errors.New("EC2 list requires a testease.runner label filter")
	}
	args := []string{
		"ec2", "describe-instances", "--filters",
		"Name=tag:" + target.LabelRunner + ",Values=" + labels[target.LabelRunner],
		"Name=instance-state-name,Values=pending,running,stopping,stopped", "--output", "json",
	}
	out, err := d.CLI.Output(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list EC2 instances: %w", err)
	}
	var response struct {
		Reservations []struct {
			Instances []struct {
				InstanceID string                        `json:"InstanceId"`
				ImageID    string                        `json:"ImageId"`
				Tags       []struct{ Key, Value string } `json:"Tags"`
			} `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return nil, fmt.Errorf("read EC2 instances: %w", err)
	}
	var machines []provisioned.Machine
	for _, reservation := range response.Reservations {
		for _, instance := range reservation.Instances {
			tags := map[string]string{}
			for _, tag := range instance.Tags {
				tags[tag.Key] = tag.Value
			}
			matches := true
			for key, value := range labels {
				if tags[key] != value {
					matches = false
					break
				}
			}
			if matches && instance.InstanceID != "" {
				machines = append(machines, provisioned.Machine{
					ID:      instance.InstanceID,
					Address: instance.InstanceID, Image: instance.ImageID, Labels: tags,
				})
			}
		}
	}
	return machines, nil
}

func (d Driver) Destroy(ctx context.Context, m provisioned.Machine) error {
	if err := d.check(); err != nil {
		return err
	}
	if m.ID == "" {
		return errors.New("cannot terminate an EC2 instance without an id")
	}
	_, err := d.CLI.Output(ctx, "ec2", "terminate-instances", "--instance-ids", m.ID, "--output", "json")
	if err != nil && !strings.Contains(err.Error(), "InvalidInstanceID.NotFound") {
		return fmt.Errorf("terminate EC2 instance %s: %w", m.ID, err)
	}
	return nil
}
