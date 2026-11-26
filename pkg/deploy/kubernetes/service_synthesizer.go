// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	kubemodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/kubernetes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewServiceSynthesizer constructs a new serviceSynthesizer.
func NewServiceSynthesizer(
	k8sClient client.Client,
	trackingProvider tracking.Provider,
	svcManager ServiceManager,
	logger logging.Logger,
	stack core.Stack,
) *serviceSynthesizer {
	return &serviceSynthesizer{
		k8sClient:        k8sClient,
		trackingProvider: trackingProvider,
		svcManager:       svcManager,
		logger:           logger,
		stack:            stack,
	}
}

// serviceSynthesizer is responsible for reconciling Kubernetes Service resources for a stack.
type serviceSynthesizer struct {
	k8sClient        client.Client
	trackingProvider tracking.Provider
	svcManager       ServiceManager
	logger           logging.Logger
	stack            core.Stack

	unmatchedK8sSvcs []*corev1.Service
}

func (s *serviceSynthesizer) Synthesize(ctx context.Context) error {
	// IR-absent mode: the Kubernetes Service is IR-derived (its ports come from
	// Gateway listeners). With no IR the builder does not add it to the stack, so
	// it would appear unmatched and be deleted — reverting the live Service.
	// Preserve the existing Service instead.
	if !s.stack.IRPresent() {
		s.logger.Info("IR absent, preserving existing Kubernetes Service (skipping service synthesis)",
			"stackID", s.stack.StackID())
		return nil
	}

	var resSvcs []*kubemodel.ServiceResource
	if err := s.stack.ListResources(&resSvcs); err != nil {
		return fmt.Errorf("failed to list service resources: %w", err)
	}

	k8sSvcs, err := s.findK8sServices(ctx)
	if err != nil {
		return err
	}

	matchedPairs, unmatchedResSvcs, unmatchedK8sSvcs := matchResAndK8sServices(resSvcs, k8sSvcs)
	s.unmatchedK8sSvcs = unmatchedK8sSvcs

	for _, resSvc := range unmatchedResSvcs {
		observed, err := s.svcManager.Create(ctx, resSvc)
		if err != nil {
			return err
		}
		resSvc.SetObservedStatus(observed)
	}
	for _, pair := range matchedPairs {
		observed, err := s.svcManager.Update(ctx, pair.resSvc, pair.k8sSvc)
		if err != nil {
			return err
		}
		pair.resSvc.SetObservedStatus(observed)
	}

	return nil
}

func (s *serviceSynthesizer) PostSynthesize(ctx context.Context) error {
	for _, k8sSvc := range s.unmatchedK8sSvcs {
		if err := s.svcManager.Delete(ctx, k8sSvc); err != nil {
			return err
		}
	}
	return nil
}

// findK8sServices lists Kubernetes Services managed by this controller for the current stack.
func (s *serviceSynthesizer) findK8sServices(ctx context.Context) ([]*corev1.Service, error) {
	stackLabels := s.trackingProvider.KubernetesSafeStackLabels(s.stack)

	svcList := &corev1.ServiceList{}
	// TODO - Add namespace filtering in.
	if err := s.k8sClient.List(ctx, svcList, client.MatchingLabels(stackLabels)); err != nil {
		return nil, fmt.Errorf("failed to list k8s services: %w", err)
	}

	out := make([]*corev1.Service, 0, len(svcList.Items))
	for i := range svcList.Items {
		out = append(out, &svcList.Items[i])
	}
	return out, nil
}

// resAndK8sServicePair holds a matched pair of desired resource and actual k8s Service.
type resAndK8sServicePair struct {
	resSvc *kubemodel.ServiceResource
	k8sSvc *corev1.Service
}

// matchResAndK8sServices pairs desired ServiceResource entries with existing k8s Services
// by namespaced name. Unmatched desired resources need creation; unmatched k8s Services
// are candidates for deletion.
func matchResAndK8sServices(
	resSvcs []*kubemodel.ServiceResource,
	k8sSvcs []*corev1.Service,
) ([]resAndK8sServicePair, []*kubemodel.ServiceResource, []*corev1.Service) {
	var matchedPairs []resAndK8sServicePair
	var unmatchedResSvcs []*kubemodel.ServiceResource
	var unmatchedK8sSvcs []*corev1.Service

	resByKey := mapResServicesByKey(resSvcs)
	k8sByKey := mapK8sServicesByKey(k8sSvcs)

	resKeys := sets.KeySet(resByKey)
	k8sKeys := sets.KeySet(k8sByKey)

	for _, k := range resKeys.Intersection(k8sKeys).UnsortedList() {
		matchedPairs = append(matchedPairs, resAndK8sServicePair{
			resSvc: resByKey[k],
			k8sSvc: k8sByKey[k],
		})
	}
	for _, k := range resKeys.Difference(k8sKeys).UnsortedList() {
		unmatchedResSvcs = append(unmatchedResSvcs, resByKey[k])
	}
	for _, k := range k8sKeys.Difference(resKeys).UnsortedList() {
		unmatchedK8sSvcs = append(unmatchedK8sSvcs, k8sByKey[k])
	}

	return matchedPairs, unmatchedResSvcs, unmatchedK8sSvcs
}

func mapResServicesByKey(resSvcs []*kubemodel.ServiceResource) map[string]*kubemodel.ServiceResource {
	out := make(map[string]*kubemodel.ServiceResource, len(resSvcs))
	for _, r := range resSvcs {
		key := types.NamespacedName{
			Namespace: r.Spec.Template.Namespace,
			Name:      r.Spec.Template.Name,
		}.String()
		out[key] = r
	}
	return out
}

func mapK8sServicesByKey(k8sSvcs []*corev1.Service) map[string]*corev1.Service {
	out := make(map[string]*corev1.Service, len(k8sSvcs))
	for _, s := range k8sSvcs {
		key := types.NamespacedName{Namespace: s.Namespace, Name: s.Name}.String()
		out[key] = s
	}
	return out
}
