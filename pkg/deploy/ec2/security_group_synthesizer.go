// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ec2

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	ec2model "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	"k8s.io/apimachinery/pkg/util/sets"
)

// SecurityGroupSynthesizer manages the lifecycle of frontend security groups in AWS.
type SecurityGroupSynthesizer interface {
	// Synthesize creates or updates frontend security groups based on the stack resources.
	Synthesize(ctx context.Context) error

	// PostSynthesize performs any post-synthesis cleanup (deferred deletions).
	PostSynthesize(ctx context.Context) error
}

type defaultSecurityGroupSynthesizer struct {
	trackingProvider tracking.Provider
	taggingManager   TaggingManager
	sgManager        SecurityGroupManager
	logger           logging.Logger
	stack            core.Stack

	// unmatchedSDKSGs holds existing SGs that no longer match any desired resource,
	// queued for deferred deletion in PostSynthesize.
	unmatchedSDKSGs []networking.SecurityGroupInfo
}

// NewSecurityGroupSynthesizer creates a new SecurityGroupSynthesizer.
func NewSecurityGroupSynthesizer(
	trackingProvider tracking.Provider,
	taggingManager TaggingManager,
	sgManager SecurityGroupManager,
	logger logging.Logger,
	stack core.Stack,
) SecurityGroupSynthesizer {
	return &defaultSecurityGroupSynthesizer{
		trackingProvider: trackingProvider,
		taggingManager:   taggingManager,
		sgManager:        sgManager,
		logger:           logger,
		stack:            stack,
	}
}

func (s *defaultSecurityGroupSynthesizer) Synthesize(ctx context.Context) error {
	// IR-absent mode: the frontend SG is an IR-derived resource (ingress rules come
	// from Gateway listener ports). With no IR the builder does not add it to the
	// stack, so it would appear unmatched and be deleted — reverting live ingress.
	// Preserve all existing SGs instead: reconcile nothing, queue no deletions.
	if !s.stack.IRPresent() {
		s.logger.Info("IR absent, preserving existing frontend security groups (skipping SG synthesis)",
			"stackID", s.stack.StackID())
		return nil
	}

	var resSGs []*ec2model.SecurityGroup
	if err := s.stack.ListResources(&resSGs); err != nil {
		return fmt.Errorf("failed to list security group resources: %w", err)
	}

	sdkSGs, err := s.findSDKSecurityGroups(ctx)
	if err != nil {
		return err
	}

	matchedResAndSDKSGs, unmatchedResSGs, unmatchedSDKSGs, err := matchResAndSDKSecurityGroups(
		resSGs, sdkSGs, s.trackingProvider.ResourceIDTagKey(),
	)
	if err != nil {
		return err
	}

	// For SecurityGroup, we delete unmatched ones during post synthesize.
	s.unmatchedSDKSGs = unmatchedSDKSGs

	for _, resSG := range unmatchedResSGs {
		sgStatus, err := s.sgManager.Create(ctx, resSG)
		if err != nil {
			return err
		}
		resSG.SetStatus(sgStatus)
	}

	for _, resAndSDKSG := range matchedResAndSDKSGs {
		sgStatus, err := s.sgManager.Update(ctx, resAndSDKSG.resSG, resAndSDKSG.sdkSG)
		if err != nil {
			return err
		}
		resAndSDKSG.resSG.SetStatus(sgStatus)
	}

	return nil
}

// findSDKSecurityGroups discovers all existing frontend SGs managed by this controller for the stack.
func (s *defaultSecurityGroupSynthesizer) findSDKSecurityGroups(ctx context.Context) ([]networking.SecurityGroupInfo, error) {
	stackTags := s.trackingProvider.StackTags(s.stack.StackID().String())
	return s.taggingManager.ListSecurityGroups(ctx, tracking.TagsAsTagFilter(stackTags))
}

// PostSynthesize performs deferred cleanup by deleting unmatched SDK SGs
// that no longer correspond to any desired resource.
//
// Deletion is attempted for every unmatched SG so that a single failure does
// not block progress on the others (e.g. duplicate SGs from a prior leak).
// All errors are accumulated and returned as a joined error, causing the
// controller reconcile loop to requeue after 30s.
func (s *defaultSecurityGroupSynthesizer) PostSynthesize(ctx context.Context) error {
	var errs []error
	for _, sg := range s.unmatchedSDKSGs {
		if err := s.sgManager.Delete(ctx, sg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resAndSDKSecurityGroupPair holds a matched pair of desired and actual security groups.
type resAndSDKSecurityGroupPair struct {
	resSG *ec2model.SecurityGroup
	sdkSG networking.SecurityGroupInfo
}

// matchResAndSDKSecurityGroups matches desired SG resources against existing SDK SGs
// using the resource ID tag key. Returns matched pairs, unmatched desired resources,
// unmatched SDK SGs (candidates for deletion), and an error if any SDK SG is missing the resource ID tag.
func matchResAndSDKSecurityGroups(
	resSGs []*ec2model.SecurityGroup,
	sdkSGs []networking.SecurityGroupInfo,
	resourceIDTagKey string,
) ([]resAndSDKSecurityGroupPair, []*ec2model.SecurityGroup, []networking.SecurityGroupInfo, error) {
	var matchedResAndSDKSGs []resAndSDKSecurityGroupPair
	var unmatchedResSGs []*ec2model.SecurityGroup
	var unmatchedSDKSGs []networking.SecurityGroupInfo

	resSGsByID := mapResSecurityGroupByResourceID(resSGs)
	sdkSGsByID, err := mapSDKSecurityGroupByResourceID(sdkSGs, resourceIDTagKey)
	if err != nil {
		return nil, nil, nil, err
	}

	resSGIDs := sets.StringKeySet(resSGsByID)
	sdkSGIDs := sets.StringKeySet(sdkSGsByID)

	for _, resID := range resSGIDs.Intersection(sdkSGIDs).List() {
		resSG := resSGsByID[resID]
		sdkSGs := sdkSGsByID[resID]
		matchedResAndSDKSGs = append(matchedResAndSDKSGs, resAndSDKSecurityGroupPair{
			resSG: resSG,
			sdkSG: sdkSGs[0],
		})
		unmatchedSDKSGs = append(unmatchedSDKSGs, sdkSGs[1:]...)
	}
	for _, resID := range resSGIDs.Difference(sdkSGIDs).List() {
		unmatchedResSGs = append(unmatchedResSGs, resSGsByID[resID])
	}
	for _, resID := range sdkSGIDs.Difference(resSGIDs).List() {
		unmatchedSDKSGs = append(unmatchedSDKSGs, sdkSGsByID[resID]...)
	}

	return matchedResAndSDKSGs, unmatchedResSGs, unmatchedSDKSGs, nil
}

func mapResSecurityGroupByResourceID(resSGs []*ec2model.SecurityGroup) map[string]*ec2model.SecurityGroup {
	resSGsByID := make(map[string]*ec2model.SecurityGroup, len(resSGs))
	for _, resSG := range resSGs {
		resSGsByID[resSG.ID()] = resSG
	}
	return resSGsByID
}

func mapSDKSecurityGroupByResourceID(sdkSGs []networking.SecurityGroupInfo, resourceIDTagKey string) (map[string][]networking.SecurityGroupInfo, error) {
	sdkSGsByID := make(map[string][]networking.SecurityGroupInfo, len(sdkSGs))
	for _, sdkSG := range sdkSGs {
		resourceID, ok := sdkSG.Tags[resourceIDTagKey]
		if !ok {
			return nil, fmt.Errorf("unexpected securityGroup with no resourceID: %v", sdkSG.SecurityGroupID)
		}
		sdkSGsByID[resourceID] = append(sdkSGsByID[resourceID], sdkSG)
	}
	return sdkSGsByID, nil
}
