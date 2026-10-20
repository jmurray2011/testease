package main

import (
	"context"
	"log/slog"

	ssmbackend "github.com/jmurray2011/testease/internal/backend/ssm"
	"github.com/jmurray2011/testease/internal/platform/ec2"
	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/target"
)

func cloudProviders(binding awsAccountBinding, runID, runner, transferStore string, log *slog.Logger) map[string]target.Provider {
	client := ssmbackend.Client{Profile: binding.Profile, Region: binding.Region,
		TransferStore: transferStore, Log: log}
	driver := ec2.Driver{CLI: client, Profile: binding.Profile, Region: binding.Region}
	provider := provisioned.New(provisioned.Options{
		Driver: driver, Name: "ec2", RunID: runID, Runner: runner,
		Dial: func(ctx context.Context, machine provisioned.Machine) (target.Session, error) {
			instance, err := client.Describe(ctx, machine.Address)
			if err != nil {
				return nil, err
			}
			return &ssmbackend.Session{Client: client, Instance: instance}, nil
		},
	})
	return map[string]target.Provider{"ec2": provider}
}
