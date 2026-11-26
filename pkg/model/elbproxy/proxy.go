// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/go-errors/errors"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
)

const (
	ResourceTypeProxy = "ProxyResource"
)

// ProxyResource is a stack resource representing an ELBProxy proxy.
type ProxyResource struct {
	core.ResourceMeta `json:"-"`

	Name                 string
	ProxyType            string
	RoleArn              string
	ProxyVersion         string
	Attributes           []elbproxyaws.Attribute
	Configuration        elbproxyaws.ProxyConfiguration
	NetworkConfiguration elbproxyaws.NetworkUserConfiguration
	SecurityGroupTokens  []core.StringToken
	Tags                 map[string]string
	CustomerKmsKeyArn    *string

	Status *ProxyResourceStatus `json:"status,omitempty"`
}

type ProxyResourceStatus struct {
	Arn             string
	DnsName         string
	LifecycleStatus string
}

func NewProxyResource(stack core.Stack, id string) (*ProxyResource, error) {
	res := &ProxyResource{
		ResourceMeta: core.NewResourceMeta(stack, ResourceTypeProxy, id),
	}
	if err := stack.AddResource(res); err != nil {
		return nil, fmt.Errorf("failed to add ProxyResource: %w", err)
	}
	res.registerDependencies(stack)
	return res, nil
}

// register dependencies for Proxy.
func (r *ProxyResource) registerDependencies(stack core.Stack) {
	for _, token := range r.SecurityGroupTokens {
		for _, dep := range token.Dependencies() {
			stack.AddDependency(dep, r)
		}
	}
}

func (r *ProxyResource) SetStatus(status ProxyResourceStatus) {
	r.Status = &status
}

// ProxyDNS returns the DNS for the ELBProxy Proxy
func (r *ProxyResource) ProxyDNS() core.StringToken {
	return core.NewResourceFieldStringToken(r, "status/elbProxyEndpoint",
		func(ctx context.Context, res core.Resource, fieldPath string) (s string, err error) {
			rr := res.(*ProxyResource)
			if rr.Status == nil {
				return "", errors.Errorf("Proxy is not fulfilled yet: %v", rr.ID())
			}
			return rr.Status.DnsName, nil
		},
	)
}

// ProxySpec holds the assembled specification for an ELBProxy proxy.
type ProxySpec struct {
	Name                 string
	Type                 string
	RoleArn              string
	ProxyVersion         string
	Attributes           []elbproxyaws.Attribute
	Tags                 map[string]string
	Configuration        elbproxyaws.ProxyConfiguration
	NetworkConfiguration elbproxyaws.NetworkUserConfiguration
	SecurityGroups       []core.StringToken
	CustomerKmsKeyArn    *string
}
