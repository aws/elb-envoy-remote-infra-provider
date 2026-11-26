// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceTypeService is the stack resource type for Kubernetes Services.
const ResourceTypeService = "K8S::ELBProxy::Service"

var _ core.Resource = &ServiceResource{}

// NewServiceResource constructs new ServiceResource resource.
func NewServiceResource(stack core.Stack, id string, spec ServiceResourceSpec, status *ServiceStatus) *ServiceResource {
	svcr := &ServiceResource{
		ResourceMeta: core.NewResourceMeta(stack, ResourceTypeService, id),
		Spec:         spec,
		Status:       status,
	}
	stack.AddResource(svcr)
	svcr.registerDependencies(stack)
	return svcr
}

func (svcr *ServiceResource) registerDependencies(stack core.Stack) {
	if svcr.Status == nil || svcr.Status.ELBProxyEndpoint == nil {
		return
	}
	for _, dep := range svcr.Status.ELBProxyEndpoint.Dependencies() {
		stack.AddDependency(dep, svcr)
	}
}

// SetObservedStatus records the observed status of the underlying k8s Service.
func (svcr *ServiceResource) SetObservedStatus(observed ObservedServiceStatus) {
	svcr.ObservedStatus = &observed
}

// ServiceResource represents a Kubernetes Service managed by the extension.
type ServiceResource struct {
	core.ResourceMeta `json:"-"`

	// desired state of the service
	Spec ServiceResourceSpec `json:"spec"`

	// desired ELBProxy-specific status (populated during build).
	// +optional
	Status *ServiceStatus `json:"status,omitempty"`

	// observed state of the synthesized k8s Service
	// +optional
	ObservedStatus *ObservedServiceStatus `json:"observedStatus,omitempty"`
}

// ServiceResourceSpec describes the desired Kubernetes Service.
type ServiceResourceSpec struct {
	// Template is the desired template for the k8s Service, including metadata and spec.
	Template ServiceTemplate `json:"template"`
}

// ServiceTemplate describes the desired metadata and spec of a Kubernetes Service.
type ServiceTemplate struct {
	// Standard object's metadata.
	metav1.ObjectMeta `json:"metadata"`

	// Specification of the Kubernetes Service.
	Spec corev1.ServiceSpec `json:"spec"`
}

// ServiceStatus describes the desired ELBProxy-specific status (e.g., LoadBalancer endpoint).
type ServiceStatus struct {
	// ELBProxyEndpoint is the dns name for the proxy
	ELBProxyEndpoint core.StringToken `json:"elbProxyEndpoint"`
}

// ObservedServiceStatus captures the reference to the synthesized Kubernetes Service.
type ObservedServiceStatus struct {
	// ServiceRef is a reference to the created Kubernetes Service.
	ServiceRef corev1.ObjectReference `json:"serviceRef"`
}
