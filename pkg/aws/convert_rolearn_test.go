// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/elbproxy"
	"github.com/stretchr/testify/assert"
)

func TestToProxyDetailsGet_RoleArn(t *testing.T) {
	out := &elbproxy.GetProxyOutput{
		Arn: new("arn:proxy"), DnsName: new("dns"), Name: new("p"), ProxyVersion: new("1.37"),
		RoleArn: new("arn:aws:iam::123456789012:role/proxy"),
	}
	assert.Equal(t, "arn:aws:iam::123456789012:role/proxy", toProxyDetails_get(out).RoleArn)

	out.RoleArn = nil
	assert.Empty(t, toProxyDetails_get(out).RoleArn, "a missing roleArn is left empty")
}
