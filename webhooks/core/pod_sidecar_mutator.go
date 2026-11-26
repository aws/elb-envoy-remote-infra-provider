// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/inject/sidecar"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/webhook"
)

const apiPathMutateSidecarPod = "/mutate-v1-pod"

// NewPodSidecarMutator returns a pod mutator that injects the elb-envoy-remote-infra-provider sidecar.
func NewPodSidecarMutator(injector *sidecar.SidecarInjector) *podSidecarMutator {
	return &podSidecarMutator{injector: injector}
}

var _ webhook.Mutator = &podSidecarMutator{}

type podSidecarMutator struct {
	injector *sidecar.SidecarInjector
}

func (m *podSidecarMutator) Prototype(_ admission.Request) (runtime.Object, error) {
	return &corev1.Pod{}, nil
}

func (m *podSidecarMutator) MutateCreate(_ context.Context, obj runtime.Object) (runtime.Object, error) {
	pod := obj.(*corev1.Pod)
	m.injector.Inject(pod)
	return pod, nil
}

func (m *podSidecarMutator) MutateUpdate(_ context.Context, obj runtime.Object, _ runtime.Object) (runtime.Object, error) {
	return obj, nil
}

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create,versions=v1,name=mpod.gateway.elbproxy.aws,sideEffects=None,webhookVersions=v1,admissionReviewVersions=v1

func (m *podSidecarMutator) SetupWithManager(mgr ctrl.Manager) {
	mgr.GetWebhookServer().Register(apiPathMutateSidecarPod, webhook.MutatingWebhookForMutator(m, mgr.GetScheme()))
}
