package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/targetcatalog"
)

type awsAccountBinding struct {
	Profile string
	Region  string
}

// awsBinding makes account selection explicit and shared between provisioning
// and the SSM transport. A catalog and an invocation cannot name different
// accounts while producing one record.
func awsBinding(cmd *cobra.Command, selection *targetcatalog.Selection) (awsAccountBinding, error) {
	profile := flagString(cmd, "aws-profile")
	region := flagString(cmd, "aws-region")
	if selection != nil {
		for _, item := range []struct {
			key, catalog string
			flag         *string
		}{
			{key: "aws.profile", catalog: selection.Profile.Provision["aws.profile"], flag: &profile},
			{key: "aws.region", catalog: selection.Profile.Provision["aws.region"], flag: &region},
		} {
			if item.catalog != "" && *item.flag != "" && *item.flag != item.catalog {
				return awsAccountBinding{}, fmt.Errorf("catalog %s=%q disagrees with AWS flag value %q", item.key, item.catalog, *item.flag)
			}
			if *item.flag == "" {
				*item.flag = item.catalog
			}
		}
	}
	return awsAccountBinding{Profile: profile, Region: region}, nil
}
