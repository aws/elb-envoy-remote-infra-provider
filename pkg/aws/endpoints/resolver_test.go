// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package endpoints

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/assert"
)

func TestAWSEndpointResolver_EndpointFor(t *testing.T) {
	configuration := map[string]string{
		ec2.ServiceID: "https://ec2.domain.com",
	}
	c := &Resolver{
		configuration: configuration,
	}

	type args struct {
		val string
	}

	tests := []struct {
		name    string
		args    args
		want    *string
		wantErr error
	}{
		{
			name: "when custom endpoint is configured",
			args: args{
				val: ec2.ServiceID,
			},
			want: aws.String(configuration[ec2.ServiceID]),
		},
		{
			name: "when custom endpoint is not configured",
			args: args{
				val: "elbproxy",
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := c.EndpointFor(tt.args.val)
			assert.Equal(t, tt.want, res)
		})
	}
}
