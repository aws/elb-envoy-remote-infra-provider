// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"errors"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	rgtsdk "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/algorithm"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/smithy-go"
)

// ProxyWithTags pairs an ELBProxy proxy's details with its tags.
type ProxyWithTags struct {
	// Proxy details from the ELBProxy API.
	ProxyDetails elbproxyaws.ProxyDetails

	// Tags associated with this proxy.
	Tags map[string]string
}

// options for ReconcileTags API.
type ReconcileTagsOptions struct {
	// CurrentTags on resources.
	// when it's nil, the TaggingManager will try to get the CurrentTags from AWS
	CurrentTags map[string]string

	// IgnoredTagKeys defines the tag keys that should be ignored.
	// these tags shouldn't be altered or deleted.
	IgnoredTagKeys []string
}

func (opts *ReconcileTagsOptions) ApplyOptions(options []ReconcileTagsOption) {
	for _, option := range options {
		option(opts)
	}
}

type ReconcileTagsOption func(opts *ReconcileTagsOptions)

// WithCurrentTags is a reconcile option that supplies current tags.
func WithCurrentTags(tags map[string]string) ReconcileTagsOption {
	return func(opts *ReconcileTagsOptions) {
		opts.CurrentTags = tags
	}
}

// WithIgnoredTagKeys is a reconcile option that configures IgnoredTagKeys.
func WithIgnoredTagKeys(ignoredTagKeys []string) ReconcileTagsOption {
	return func(opts *ReconcileTagsOptions) {
		opts.IgnoredTagKeys = append(opts.IgnoredTagKeys, ignoredTagKeys...)
	}
}

// TaggingManager handles tag operations for ELBProxy API resources.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination tagging_manager_mock.go -package elbproxy github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy TaggingManager
type TaggingManager interface {
	// ReconcileTags reconciles tags on an ELBProxy resource identified by ARN.
	ReconcileTags(ctx context.Context, resourceArn string, desiredTags map[string]string, opts ...ReconcileTagsOption) error

	// ListProxies returns the proxies that match any of the tag filters. A proxy matches a filter
	// when it has every key in the filter with one of the key's values (any value if none are
	// listed). With no filters, it returns no proxies.
	ListProxies(ctx context.Context, tagFilters ...tracking.TagFilter) ([]ProxyWithTags, error)
}

// NewDefaultTaggingManager constructs a new defaultTaggingManager. ListProxies finds proxies
// with rgt, the Resource Groups Tagging API.
func NewDefaultTaggingManager(client elbproxyaws.ELBProxyClient, rgt services.RGT, logger logging.Logger) *defaultTaggingManager {
	return &defaultTaggingManager{
		client: client,
		rgt:    rgt,
		logger: logger,
	}
}

var _ TaggingManager = &defaultTaggingManager{}

// defaultTaggingManager is the default implementation of TaggingManager for ELBProxy API resources.
type defaultTaggingManager struct {
	client elbproxyaws.ELBProxyClient
	rgt    services.RGT
	logger logging.Logger
}

func (m *defaultTaggingManager) ReconcileTags(ctx context.Context, resourceArn string, desiredTags map[string]string, opts ...ReconcileTagsOption) error {
	reconcileOpts := ReconcileTagsOptions{
		CurrentTags:    nil,
		IgnoredTagKeys: nil,
	}
	reconcileOpts.ApplyOptions(opts)

	currentTags := reconcileOpts.CurrentTags
	if currentTags == nil {
		resp, err := m.client.ListTagsForResource(ctx, elbproxyaws.ListTagsForResourceInput{
			ResourceArn: resourceArn,
		})
		if err != nil {
			return err
		}
		currentTags = resp.Tags
	}

	tagsToUpdate, tagsToRemove := algorithm.DiffStringMapIgnoreAWSTags(desiredTags, currentTags)
	for _, ignoredTagKey := range reconcileOpts.IgnoredTagKeys {
		delete(tagsToUpdate, ignoredTagKey)
		delete(tagsToRemove, ignoredTagKey)
	}

	if len(tagsToUpdate) > 0 {
		m.logger.Info("adding resource tags",
			"resourceArn", resourceArn,
			"change", tagsToUpdate)
		if err := m.client.TagResource(ctx, elbproxyaws.TagResourceInput{
			ResourceArn: resourceArn,
			Tags:        tagsToUpdate,
		}); err != nil {
			return err
		}
		m.logger.Info("added resource tags",
			"resourceArn", resourceArn)
	}

	if len(tagsToRemove) > 0 {
		tagKeysToRemove := make([]string, 0, len(tagsToRemove))
		for key := range tagsToRemove {
			tagKeysToRemove = append(tagKeysToRemove, key)
		}
		m.logger.Info("removing resource tags",
			"resourceArn", resourceArn,
			"change", tagsToRemove)
		if err := m.client.UntagResource(ctx, elbproxyaws.UntagResourceInput{
			ResourceArn: resourceArn,
			TagKeys:     tagKeysToRemove,
		}); err != nil {
			return err
		}
		m.logger.Info("removed resource tags",
			"resourceArn", resourceArn)
	}

	return nil
}

// ListProxies calls GetResources once per tag filter and GetProxy for each proxy found, using
// the tags that GetResources returns.
func (m *defaultTaggingManager) ListProxies(ctx context.Context, tagFilters ...tracking.TagFilter) ([]ProxyWithTags, error) {
	// use a map to avoid potential duplication in returned resources
	resourceTagsByARN := make(map[string][]rgttypes.Tag)
	for _, tagFilter := range tagFilters {
		req := &rgtsdk.GetResourcesInput{
			TagFilters:          convertTagFiltersToRGTTagFilters(tagFilter),
			ResourceTypeFilters: []string{services.ResourceTypeELBProxy},
		}
		resources, err := m.rgt.GetResourcesAsList(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, resource := range resources {
			if _, exists := resourceTagsByARN[awssdk.ToString(resource.ResourceARN)]; !exists {
				resourceTagsByARN[awssdk.ToString(resource.ResourceARN)] = resource.Tags
			}
		}
	}
	var matchedProxies []ProxyWithTags
	for resourceARN, resourceTags := range resourceTagsByARN {
		details, err := m.client.GetProxy(ctx, resourceARN)
		if isProxyNotFoundError(err) {
			// GetResources can keep returning a proxy for a while after it's deleted.
			m.logger.V(1).Info("skipping proxy that GetResources returned but no longer exists", "proxyArn", resourceARN)
			continue
		}
		if err != nil {
			return nil, err
		}
		matchedProxies = append(matchedProxies, ProxyWithTags{
			ProxyDetails: *details,
			Tags:         services.ParseRGTTags(resourceTags),
		})
	}
	return matchedProxies, nil
}

// convertTagFiltersToRGTTagFilters converts a tag filter to GetResources tag filters.
func convertTagFiltersToRGTTagFilters(tagFilter tracking.TagFilter) []rgttypes.TagFilter {
	var rgtTagFilters []rgttypes.TagFilter
	for k, v := range tagFilter {
		rgtTagFilters = append(rgtTagFilters, rgttypes.TagFilter{
			Key:    awssdk.String(k),
			Values: v,
		})
	}
	return rgtTagFilters
}

// isProxyNotFoundError reports whether err is the ELBProxy API's not-found error.
func isProxyNotFoundError(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "ResourceNotFoundException"
}
