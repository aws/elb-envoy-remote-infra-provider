// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
)

func envNames(env []corev1.EnvVar) []string {
	names := make([]string, 0, len(env))
	for _, e := range env {
		names = append(names, e.Name)
	}
	return names
}

// The webhook host's own cluster/AWS settings are NOT forwarded; the sidecar
// gets only --sidecar-extra-args, verbatim and in order.
func TestSidecarInjectorConfig_PassesOnlySidecarExtraArgs(t *testing.T) {
	cfg := &config.ControllerConfig{
		ClusterName: "host-cluster",
		AWSConfig: elbproxyaws.CloudConfig{
			Region:       "us-west-2",
			VpcID:        "vpc-host",
			AWSEndpoints: map[string]string{"EC2": "https://ec2.example.com"},
		},
		WebhookConfig: config.WebhookConfig{SidecarExtraArgs: []string{
			"--cluster-name=my-cluster",
			"--aws-region=us-east-1",
			"--default-tags=team=a,env=b",
		}},
	}

	got := sidecarInjectorConfig(cfg, "envoy-gateway-system")

	assert.Equal(t, []string{"--cluster-name=my-cluster", "--aws-region=us-east-1", "--default-tags=team=a,env=b"}, got.ExtraArgs)
	assert.Equal(t, []string{"ENVOY_GATEWAY_NAMESPACE", "POD_NAMESPACE"}, envNames(got.Env))
	assert.Equal(t, "envoy-gateway-system", got.Env[0].Value)
}

func TestSidecarInjectorConfig_NoExtraArgs(t *testing.T) {
	got := sidecarInjectorConfig(&config.ControllerConfig{}, "envoy-gateway-system")
	assert.Empty(t, got.ExtraArgs)
}
