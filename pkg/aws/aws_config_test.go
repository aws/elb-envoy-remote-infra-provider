// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/version"
	smithyendpoints "github.com/aws/smithy-go/endpoints"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAWSConfig(t *testing.T) {
	tests := []struct {
		name       string
		cfg        CloudConfig
		wantRegion string
	}{
		{
			name:       "sets region",
			cfg:        CloudConfig{Region: "us-east-1"},
			wantRegion: "us-east-1",
		},
		{
			name:       "different region",
			cfg:        CloudConfig{Region: "eu-west-1"},
			wantRegion: "eu-west-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewAWSConfigGenerator(tt.cfg)
			awsCfg, err := gen.GenerateAWSConfig(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.wantRegion, awsCfg.Region)
		})
	}
}

func TestGenerateAWSConfig_MaxAttempts(t *testing.T) {
	tests := []struct {
		name            string
		maxAttempts     int
		wantMaxAttempts int
	}{
		{name: "3 attempts", maxAttempts: 3, wantMaxAttempts: 3},
		{name: "10 attempts", maxAttempts: 10, wantMaxAttempts: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewAWSConfigGenerator(CloudConfig{Region: "us-east-1", MaxAttempts: tt.maxAttempts})
			awsCfg, err := gen.GenerateAWSConfig(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.wantMaxAttempts, awsCfg.RetryMaxAttempts)
		})
	}
}

func TestGenerateAWSConfig_OptFnsApplied(t *testing.T) {
	gen := NewAWSConfigGenerator(CloudConfig{Region: "us-east-1", MaxAttempts: 3})

	// Caller-supplied opts are appended after defaults, so they can override them.
	awsCfg, err := gen.GenerateAWSConfig(context.Background(), config.WithRegion("us-west-2"))
	require.NoError(t, err)
	assert.Equal(t, "us-west-2", awsCfg.Region, "caller optFns should override default region")
}

type staticEC2EndpointResolver struct{ url string }

func (r staticEC2EndpointResolver) ResolveEndpoint(_ context.Context, _ ec2.EndpointParameters) (smithyendpoints.Endpoint, error) {
	u, err := url.Parse(r.url)
	if err != nil {
		return smithyendpoints.Endpoint{}, err
	}
	return smithyendpoints.Endpoint{URI: *u}, nil
}

func TestGenerateAWSConfig_UserAgentHeader(t *testing.T) {
	tests := []struct {
		name       string
		gitVersion string
		wantUA     string
	}{
		{
			name:       "user agent is exactly elbproxy.k8s.aws/<version>",
			gitVersion: "v2.14.1",
			wantUA:     "elbproxy.k8s.aws/v2.14.1",
		},
		{
			name:       "user agent reflects updated git version",
			gitVersion: "v3.0.0",
			wantUA:     "elbproxy.k8s.aws/v3.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version.GitVersion = tt.gitVersion

			var capturedUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedUA = r.Header.Get("User-Agent")
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer srv.Close()

			gen := NewAWSConfigGenerator(CloudConfig{Region: "us-east-1", MaxAttempts: 1})
			awsCfg, err := gen.GenerateAWSConfig(
				context.Background(),
				config.WithCredentialsProvider(aws.AnonymousCredentials{}),
			)
			require.NoError(t, err)

			ec2Client := ec2.NewFromConfig(awsCfg, func(o *ec2.Options) {
				o.EndpointResolverV2 = staticEC2EndpointResolver{url: srv.URL}
			})
			_, _ = ec2Client.DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})

			require.NotEmpty(t, capturedUA, "no request was captured")
			assert.Equal(t, tt.wantUA, capturedUA)
		})
	}
}
