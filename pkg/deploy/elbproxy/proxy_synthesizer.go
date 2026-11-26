// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"
	"time"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	// proxyProvisioningRequeueDelay is how long to wait before re-checking a proxy
	// that is still being provisioned.
	proxyProvisioningRequeueDelay = 2 * time.Minute

	// proxyDeletionRequeueDelay is how long to wait for a deleting proxy to be gone
	// before creating its replacement.
	proxyDeletionRequeueDelay = 30 * time.Second
)

func NewProxySynthesizer(
	trackingProvider tracking.Provider,
	taggingManager TaggingManager,
	proxyManager ProxyManager,
	logger logging.Logger,
	stack core.Stack,
) *proxySynthesizer {
	return &proxySynthesizer{
		trackingProvider: trackingProvider,
		taggingManager:   taggingManager,
		proxyManager:     proxyManager,
		logger:           logger,
		stack:            stack,
	}
}

type proxySynthesizer struct {
	trackingProvider    tracking.Provider
	taggingManager      TaggingManager
	proxyManager        ProxyManager
	logger              logging.Logger
	stack               core.Stack
	proxyDetails        *elbproxyaws.ProxyDetails
	unmatchedSDKProxies []ProxyWithTags
	// teardown is true when the stack has no desired proxy (gateway deleted).
	teardown bool
}

func (s *proxySynthesizer) Synthesize(ctx context.Context) error {
	var resProxies []*elbproxymodel.ProxyResource
	if err := s.stack.ListResources(&resProxies); err != nil {
		return fmt.Errorf("failed to list proxy resources: %w", err)
	}

	sdkProxies, err := s.findSDKProxies(ctx)
	if err != nil {
		return err
	}

	// Prefer non-deleting matches while retaining deleting duplicates for cleanup.
	remainingProxiesByID := mapResProxyByResourceID(resProxies)
	orderedSDKProxies := make([]ProxyWithTags, 0, len(sdkProxies))
	for _, sdkProxy := range sdkProxies {

		if sdkProxy.ProxyDetails.Status.Code != elbproxyaws.ProxyStatusDeleteInProgress {
			orderedSDKProxies = append(orderedSDKProxies, sdkProxy)
			delete(remainingProxiesByID, sdkProxy.Tags[s.trackingProvider.ResourceIDTagKey()])
		}
	}
	for _, sdkProxy := range sdkProxies {
		if sdkProxy.ProxyDetails.Status.Code != elbproxyaws.ProxyStatusDeleteInProgress {
			continue
		}
		if _, desired := remainingProxiesByID[sdkProxy.Tags[s.trackingProvider.ResourceIDTagKey()]]; desired {
			return reconciler.NewRequeueNeededAfter(reconciler.RequeueReasonPreviousProxyDeleting, proxyDeletionRequeueDelay)
		}
		orderedSDKProxies = append(orderedSDKProxies, sdkProxy)
	}
	sdkProxies = orderedSDKProxies

	matchedResAndSDKProxies, unmatchedResProxies, unmatchedSDKProxies, err := matchResAndSDKProxies(
		resProxies, sdkProxies, s.trackingProvider.ResourceIDTagKey(),
	)
	if err != nil {
		return err
	}

	s.unmatchedSDKProxies = unmatchedSDKProxies
	s.teardown = len(resProxies) == 0

	// Replace: delete the old proxy, then create the new one in this pass. If the name is
	// unchanged (explicit spec.name), requeue until the async delete frees it.
	var toUpdate []resAndSDKProxyPair
	for _, pair := range matchedResAndSDKProxies {
		if !isSDKProxyRequiresReplacement(pair.sdkProxy, pair.resProxy) {
			toUpdate = append(toUpdate, pair)
			continue
		}
		s.logger.Info("replacing proxy", "name", pair.sdkProxy.ProxyDetails.Name, "arn", pair.sdkProxy.ProxyDetails.Arn,
			"newName", pair.resProxy.Name)
		if err := s.proxyManager.Delete(ctx, pair.sdkProxy); err != nil {
			return err
		}
		if pair.sdkProxy.ProxyDetails.Name == pair.resProxy.Name {
			return reconciler.NewRequeueNeededAfter(reconciler.RequeueReasonPreviousProxyDeleting, proxyDeletionRequeueDelay)
		}
		unmatchedResProxies = append(unmatchedResProxies, pair.resProxy)
	}
	matchedResAndSDKProxies = toUpdate

	for _, resProxy := range unmatchedResProxies {
		proxyStatus, sdkProxy, err := s.proxyManager.Create(ctx, resProxy)
		if err != nil {
			return err
		}
		resProxy.SetStatus(proxyStatus)
		if s.proxyDetails == nil {
			details := sdkProxy.ProxyDetails
			s.proxyDetails = &details
		}
	}

	for _, resAndSDKProxy := range matchedResAndSDKProxies {
		proxyStatus, err := s.proxyManager.Update(ctx, resAndSDKProxy.resProxy, resAndSDKProxy.sdkProxy)
		if err != nil {
			return err
		}
		resAndSDKProxy.resProxy.SetStatus(proxyStatus)
		if s.proxyDetails == nil {
			details := resAndSDKProxy.sdkProxy.ProxyDetails
			s.proxyDetails = &details
		}
	}

	// Gate downstream resources (e.g. the Kubernetes Service) on proxy readiness.
	// The Service is created during serviceSynthesizer.Synthesize(), which the stack
	// deployer runs *after* this synthesizer in the same Synthesize pass. Returning the
	// requeue here aborts that pass before the Service is created, so the Service is only
	// created once the proxy has finished provisioning. (Returning it from PostSynthesize
	// is too late — the Service has already been created by then.)
	if s.proxyDetails != nil && s.proxyDetails.Status.Code == elbproxyaws.ProxyStatusCreateInProgress {
		return reconciler.NewRequeueNeededAfter(reconciler.RequeueReasonProxyProvisioning, proxyProvisioningRequeueDelay)
	}

	return nil
}

func (s *proxySynthesizer) PostSynthesize(ctx context.Context) error {
	for _, proxy := range s.unmatchedSDKProxies {
		// Already being deleted; calling DeleteProxy again is a no-op.
		if proxy.ProxyDetails.Status.Code == elbproxyaws.ProxyStatusDeleteInProgress {
			continue
		}
		if err := s.proxyManager.Delete(ctx, proxy); err != nil {
			s.logger.Error(err, "failed to delete unmatched proxy", "proxyArn", proxy.ProxyDetails.Arn)
		}
	}
	// On teardown, a still-listed proxy's ENIs still use our SGs: requeue so SG cleanup waits for it to be gone.
	if s.teardown && len(s.unmatchedSDKProxies) > 0 {
		return reconciler.NewRequeueNeededAfter(reconciler.RequeueReasonWaitingForProxyDeletion, proxyDeletionRequeueDelay)
	}
	return nil
}

func (s *proxySynthesizer) GetProxyDetails() *elbproxyaws.ProxyDetails {
	return s.proxyDetails
}

// isSDKProxyRequiresReplacement reports whether the name, ipConfiguration or roleArn differs from
// the live proxy. Skips values the API didn't return. Never replaces when IR is absent: the IP
// defaults (and so the generated name) come from the EnvoyProxy IPFamily.
func isSDKProxyRequiresReplacement(sdkProxy ProxyWithTags, resProxy *elbproxymodel.ProxyResource) bool {
	if !resProxy.Stack().IRPresent() {
		return false
	}
	if liveName := sdkProxy.ProxyDetails.Name; liveName != "" && liveName != resProxy.Name {
		return true
	}
	liveIP := sdkProxy.ProxyDetails.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration
	if liveIP != (elbproxyaws.IpAddressingConfiguration{}) &&
		liveIP != resProxy.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration {
		return true
	}
	liveRole := sdkProxy.ProxyDetails.RoleArn
	return liveRole != "" && liveRole != resProxy.RoleArn
}

// resAndSDKProxyPair holds a matched pair of desired and actual proxies.
type resAndSDKProxyPair struct {
	resProxy *elbproxymodel.ProxyResource
	sdkProxy ProxyWithTags
}

// findSDKProxies discovers all existing proxies managed by this controller for the stack.
func (s *proxySynthesizer) findSDKProxies(ctx context.Context) ([]ProxyWithTags, error) {
	stackTags := s.trackingProvider.StackTags(s.stack.StackID().String())
	return s.taggingManager.ListProxies(ctx, tracking.TagsAsTagFilter(stackTags))
}

func matchResAndSDKProxies(
	resProxies []*elbproxymodel.ProxyResource,
	sdkProxies []ProxyWithTags,
	resourceIDTagKey string,
) ([]resAndSDKProxyPair, []*elbproxymodel.ProxyResource, []ProxyWithTags, error) {
	var matchedResAndSDKProxies []resAndSDKProxyPair
	var unmatchedResProxies []*elbproxymodel.ProxyResource
	var unmatchedSDKProxies []ProxyWithTags

	resProxiesByID := mapResProxyByResourceID(resProxies)
	sdkProxiesByID, err := mapSDKProxyByResourceID(sdkProxies, resourceIDTagKey)
	if err != nil {
		return nil, nil, nil, err
	}

	resProxyIDs := sets.StringKeySet(resProxiesByID)
	sdkProxyIDs := sets.StringKeySet(sdkProxiesByID)

	for _, resID := range resProxyIDs.Intersection(sdkProxyIDs).List() {
		resProxy := resProxiesByID[resID]
		sdkMatches := sdkProxiesByID[resID]
		matchedResAndSDKProxies = append(matchedResAndSDKProxies, resAndSDKProxyPair{
			resProxy: resProxy,
			sdkProxy: sdkMatches[0],
		})
		unmatchedSDKProxies = append(unmatchedSDKProxies, sdkMatches[1:]...)
	}
	for _, resID := range resProxyIDs.Difference(sdkProxyIDs).List() {
		unmatchedResProxies = append(unmatchedResProxies, resProxiesByID[resID])
	}
	for _, resID := range sdkProxyIDs.Difference(resProxyIDs).List() {
		unmatchedSDKProxies = append(unmatchedSDKProxies, sdkProxiesByID[resID]...)
	}

	return matchedResAndSDKProxies, unmatchedResProxies, unmatchedSDKProxies, nil
}

func mapResProxyByResourceID(resProxies []*elbproxymodel.ProxyResource) map[string]*elbproxymodel.ProxyResource {
	resProxiesByID := make(map[string]*elbproxymodel.ProxyResource, len(resProxies))
	for _, resProxy := range resProxies {
		resProxiesByID[resProxy.ID()] = resProxy
	}
	return resProxiesByID
}

func mapSDKProxyByResourceID(sdkProxies []ProxyWithTags, resourceIDTagKey string) (map[string][]ProxyWithTags, error) {
	sdkProxiesByID := make(map[string][]ProxyWithTags, len(sdkProxies))
	for _, sdkProxy := range sdkProxies {
		resourceID, ok := sdkProxy.Tags[resourceIDTagKey]
		if !ok {
			return nil, fmt.Errorf("unexpected proxy with no resourceID: %v", sdkProxy.ProxyDetails.Arn)
		}
		sdkProxiesByID[resourceID] = append(sdkProxiesByID[resourceID], sdkProxy)
	}
	return sdkProxiesByID, nil
}
