// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/algorithm"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
)

// ProxyAttributeReconciler reconciles proxy attributes to match the desired state.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination proxy_attribute_reconciler_mock.go -package elbproxy github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy ProxyAttributeReconciler
type ProxyAttributeReconciler interface {
	Reconcile(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) error
}

// NewDefaultProxyAttributeReconciler constructs a new defaultProxyAttributeReconciler.
func NewDefaultProxyAttributeReconciler(client elbproxyaws.ELBProxyClient, logger logging.Logger) *defaultProxyAttributeReconciler {
	return &defaultProxyAttributeReconciler{
		client: client,
		logger: logger,
	}
}

var _ ProxyAttributeReconciler = &defaultProxyAttributeReconciler{}

type defaultProxyAttributeReconciler struct {
	client elbproxyaws.ELBProxyClient
	logger logging.Logger
}

func (r *defaultProxyAttributeReconciler) Reconcile(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) error {
	desiredAttrs := r.getDesiredAttributes(resProxy)
	currentAttrs, defaultAttrs, err := r.getCurrentAttributes(ctx, sdkProxy)
	if err != nil {
		return err
	}

	attributesToUpdate, attributesToRemove := algorithm.DiffStringMap(desiredAttrs, currentAttrs)

	// Read-only attributes cannot be modified after creation. Error out to surface the misconfiguration.
	for key := range attributesToUpdate {
		if currentVal, existsInCurrent := currentAttrs[key]; existsInCurrent {
			if _, mutable := defaultAttrs[key]; !mutable {
				return fmt.Errorf("attribute %q is read-only and cannot be changed from %q to %q",
					key, currentVal, attributesToUpdate[key])
			}
		}
	}

	// For mutable attributes removed from the CRD, reset them to their default value
	// if the current value differs from the default.
	for key, currentVal := range attributesToRemove {
		if defaultVal, ok := defaultAttrs[key]; ok && currentVal != defaultVal {
			attributesToUpdate[key] = defaultVal
		}
	}

	if len(attributesToUpdate) == 0 {
		return nil
	}

	attrs := make([]elbproxyaws.Attribute, 0, len(attributesToUpdate))
	for key, val := range attributesToUpdate {
		attrs = append(attrs, elbproxyaws.Attribute{Key: key, Value: val})
	}

	r.logger.Info("modifying proxy attributes",
		"stackID", resProxy.Stack().StackID(),
		"resourceID", resProxy.ID(),
		"arn", sdkProxy.ProxyDetails.Arn,
		"change", attributesToUpdate)

	if _, err := r.client.UpdateProxyAttributes(ctx, elbproxyaws.UpdateProxyAttributesInput{
		ProxyIdentifier: sdkProxy.ProxyDetails.Arn,
		Attributes:      attrs,
	}); err != nil {
		return fmt.Errorf("failed to update proxy attributes: %w", err)
	}

	r.logger.Info("modified proxy attributes",
		"stackID", resProxy.Stack().StackID(),
		"resourceID", resProxy.ID(),
		"arn", sdkProxy.ProxyDetails.Arn)

	return nil
}

func (r *defaultProxyAttributeReconciler) getDesiredAttributes(resProxy *elbproxymodel.ProxyResource) map[string]string {
	attrs := make(map[string]string, len(resProxy.Attributes))
	for _, a := range resProxy.Attributes {
		attrs[a.Key] = a.Value
	}
	return attrs
}

// getCurrentAttributes returns two maps: current configured values and default values.
// Attributes whose DefaultValue is nil are omitted from the defaults map (e.g. read-only attributes).
func (r *defaultProxyAttributeReconciler) getCurrentAttributes(ctx context.Context, sdkProxy ProxyWithTags) (map[string]string, map[string]string, error) {
	resp, err := r.client.GetProxyAttributes(ctx, elbproxyaws.GetProxyAttributesInput{
		ProxyIdentifier: sdkProxy.ProxyDetails.Arn,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list proxy attributes: %w", err)
	}

	attrs := make(map[string]string, len(resp.Attributes))
	defaults := make(map[string]string)
	for _, a := range resp.Attributes {
		attrs[a.Key] = a.Value
		if a.DefaultValue != nil {
			defaults[a.Key] = *a.DefaultValue
		}
	}
	return attrs, defaults, nil
}
