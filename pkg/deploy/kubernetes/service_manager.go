// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/algorithm"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	kubemodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/kubernetes"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ServiceFinalizer is attached to every Service this controller creates. While it
// is present, a user-issued `kubectl delete` will stall (deletionTimestamp set
// but object not reaped) until this controller removes the finalizer during its
// own Delete flow, ensuring the cloud-side teardown runs first.
const ServiceFinalizer = "gateway.elbproxy.aws/service-finalizer"

// serviceRecreateRequeueDelay gives the API server time to reap a terminating Service
// before the next pass recreates it.
const serviceRecreateRequeueDelay = 5 * time.Second

// ServiceManager is responsible for create/update/delete Kubernetes Service resources.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination service_manager_mock.go -package kubernetes github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/kubernetes ServiceManager
type ServiceManager interface {
	Create(ctx context.Context, resSvc *kubemodel.ServiceResource) (kubemodel.ObservedServiceStatus, error)
	Update(ctx context.Context, resSvc *kubemodel.ServiceResource, k8sSvc *corev1.Service) (kubemodel.ObservedServiceStatus, error)
	Delete(ctx context.Context, k8sSvc *corev1.Service) error
}

// NewDefaultServiceManager constructs a new defaultServiceManager.
func NewDefaultServiceManager(
	k8sClient client.Client,
	trackingProvider tracking.Provider,
	logger logging.Logger,
) *defaultServiceManager {
	return &defaultServiceManager{
		k8sClient:        k8sClient,
		trackingProvider: trackingProvider,
		logger:           logger,
	}
}

var _ ServiceManager = &defaultServiceManager{}

// default implementation for ServiceManager.
type defaultServiceManager struct {
	k8sClient        client.Client
	trackingProvider tracking.Provider
	logger           logging.Logger
}

func (m *defaultServiceManager) Create(ctx context.Context, resSvc *kubemodel.ServiceResource) (kubemodel.ObservedServiceStatus, error) {
	if resSvc == nil {
		return kubemodel.ObservedServiceStatus{}, fmt.Errorf("resSvc is nil")
	}

	k8sSvc, err := m.buildK8sService(resSvc)
	if err != nil {
		return kubemodel.ObservedServiceStatus{}, err
	}
	// Attach the finalizer up front so we never observe a Service that we own
	// without the finalizer, even if the controller crashes immediately after create.
	controllerutil.AddFinalizer(k8sSvc, ServiceFinalizer)

	m.logger.Info("creating service",
		"stackID", resSvc.Stack().StackID(),
		"resourceID", resSvc.ID(),
		"service", namespacedName(k8sSvc))
	if err := m.k8sClient.Create(ctx, k8sSvc); err != nil {
		return kubemodel.ObservedServiceStatus{}, fmt.Errorf("failed to create service: %w", err)
	}
	m.logger.Info("created service",
		"stackID", resSvc.Stack().StackID(),
		"resourceID", resSvc.ID(),
		"service", namespacedName(k8sSvc))

	if err := m.reconcileLoadBalancerStatus(ctx, resSvc, k8sSvc); err != nil {
		return kubemodel.ObservedServiceStatus{}, err
	}

	return buildObservedServiceStatus(k8sSvc), nil
}

// Update reconciles only the fields the controller owns. It deep-copies the existing
// Service, mutates the owned fields in place when they differ from the desired state,
// and then uses client.MergeFrom so the generated patch contains only those fields.
// Cluster-assigned fields (ClusterIP, NodePorts, etc.) are left untouched.
//
// Service status (LoadBalancer.Ingress hostname) is reconciled separately via the
// status subresource so the ELBProxy proxy DNS is refreshed whenever it drifts.
//
// The controller's finalizer is backfilled here for Services that pre-date this
// logic, and the update is skipped entirely if the Service is being deleted so we
// don't race with the Delete flow.
func (m *defaultServiceManager) Update(ctx context.Context, resSvc *kubemodel.ServiceResource, k8sSvc *corev1.Service) (kubemodel.ObservedServiceStatus, error) {
	if !k8sSvc.DeletionTimestamp.IsZero() {
		// Terminating but still desired (gateway re-applied mid-teardown, or Service deleted by
		// hand): nothing else removes our finalizer, so finish the delete and requeue to recreate.
		if err := m.Delete(ctx, k8sSvc); err != nil {
			return kubemodel.ObservedServiceStatus{}, err
		}
		return kubemodel.ObservedServiceStatus{}, reconciler.NewRequeueNeededAfter(
			fmt.Sprintf("service %s was terminating, recreating it", namespacedName(k8sSvc)), serviceRecreateRequeueDelay)
	}

	oldObj := k8sSvc.DeepCopy()

	desiredLabels := m.calculateLabels(resSvc)
	desiredAnnotations := resSvc.Spec.Template.Annotations
	desiredSpec := resSvc.Spec.Template.Spec

	needsUpdate := false

	if controllerutil.AddFinalizer(k8sSvc, ServiceFinalizer) {
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(k8sSvc.Labels, desiredLabels) {
		k8sSvc.Labels = desiredLabels
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(k8sSvc.Annotations, desiredAnnotations) {
		k8sSvc.Annotations = desiredAnnotations
		needsUpdate = true
	}
	if k8sSvc.Spec.Type != desiredSpec.Type {
		k8sSvc.Spec.Type = desiredSpec.Type
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(k8sSvc.Spec.LoadBalancerClass, desiredSpec.LoadBalancerClass) {
		k8sSvc.Spec.LoadBalancerClass = desiredSpec.LoadBalancerClass
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(k8sSvc.Spec.AllocateLoadBalancerNodePorts, desiredSpec.AllocateLoadBalancerNodePorts) {
		k8sSvc.Spec.AllocateLoadBalancerNodePorts = desiredSpec.AllocateLoadBalancerNodePorts
		needsUpdate = true
	}
	if !servicePortsEqual(k8sSvc.Spec.Ports, desiredSpec.Ports) {
		k8sSvc.Spec.Ports = mergeServicePorts(k8sSvc.Spec.Ports, desiredSpec.Ports)
		needsUpdate = true
	}

	if needsUpdate {
		m.logger.Info("modifying service",
			"stackID", resSvc.Stack().StackID(),
			"resourceID", resSvc.ID(),
			"service", namespacedName(k8sSvc))
		if err := m.k8sClient.Patch(ctx, k8sSvc, client.MergeFrom(oldObj)); err != nil {
			return kubemodel.ObservedServiceStatus{}, fmt.Errorf("failed to patch service: %w", err)
		}
		m.logger.Info("modified service",
			"stackID", resSvc.Stack().StackID(),
			"resourceID", resSvc.ID(),
			"service", namespacedName(k8sSvc))
	}

	if err := m.reconcileLoadBalancerStatus(ctx, resSvc, k8sSvc); err != nil {
		return kubemodel.ObservedServiceStatus{}, err
	}

	return buildObservedServiceStatus(k8sSvc), nil
}

// Delete removes the Service. Because the Service carries our finalizer, the
// initial Delete call only marks the object for deletion (sets
// deletionTimestamp). We then remove the finalizer to let Kubernetes actually
// reap the object. This ordering guarantees no other controller can delete a
// Service we own without going through this path first.
func (m *defaultServiceManager) Delete(ctx context.Context, k8sSvc *corev1.Service) error {
	// Issue the delete if the object isn't already terminating. With the
	// finalizer in place this just sets deletionTimestamp.
	if k8sSvc.DeletionTimestamp.IsZero() {
		m.logger.Info("deleting service", "service", namespacedName(k8sSvc))
		if err := m.k8sClient.Delete(ctx, k8sSvc); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to delete service %s: %w", namespacedName(k8sSvc), err)
		}
	}

	// Drop the finalizer so the API server can finalize the deletion.
	if controllerutil.ContainsFinalizer(k8sSvc, ServiceFinalizer) {
		oldObj := k8sSvc.DeepCopy()
		controllerutil.RemoveFinalizer(k8sSvc, ServiceFinalizer)
		if err := m.k8sClient.Patch(ctx, k8sSvc, client.MergeFrom(oldObj)); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to remove finalizer from service %s: %w", namespacedName(k8sSvc), err)
		}
	}

	m.logger.Info("deleted service", "service", namespacedName(k8sSvc))
	return nil
}

// reconcileLoadBalancerStatus writes the ELBProxy proxy DNS into the Service's
// status.loadBalancer.ingress so consumers see the current endpoint. It patches
// the status subresource only when the resolved hostname differs from what's
// already on the Service, which handles DNS drift (e.g., when the proxy is
// re-provisioned and gets a new DNS name).
func (m *defaultServiceManager) reconcileLoadBalancerStatus(ctx context.Context, resSvc *kubemodel.ServiceResource, k8sSvc *corev1.Service) error {
	if resSvc.Status == nil || resSvc.Status.ELBProxyEndpoint == nil {
		return nil
	}

	desiredHostname, err := resSvc.Status.ELBProxyEndpoint.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("failed to resolve elbproxy endpoint for service %s: %w", namespacedName(k8sSvc), err)
	}
	if desiredHostname == "" {
		return nil
	}

	desiredIngress := []corev1.LoadBalancerIngress{{Hostname: desiredHostname}}
	if equality.Semantic.DeepEqual(k8sSvc.Status.LoadBalancer.Ingress, desiredIngress) {
		return nil
	}

	oldObj := k8sSvc.DeepCopy()
	k8sSvc.Status.LoadBalancer.Ingress = desiredIngress

	m.logger.Info("updating service load balancer status",
		"stackID", resSvc.Stack().StackID(),
		"resourceID", resSvc.ID(),
		"service", namespacedName(k8sSvc),
		"hostname", desiredHostname)
	if err := m.k8sClient.Status().Patch(ctx, k8sSvc, client.MergeFrom(oldObj)); err != nil {
		return fmt.Errorf("failed to patch service status for %s: %w", namespacedName(k8sSvc), err)
	}
	return nil
}

func (m *defaultServiceManager) buildK8sService(resSvc *kubemodel.ServiceResource) (*corev1.Service, error) {
	name := resSvc.Spec.Template.Name
	namespace := resSvc.Spec.Template.Namespace
	if name == "" {
		return nil, fmt.Errorf("service name is required for resource %s", resSvc.ID())
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      m.calculateLabels(resSvc),
			Annotations: resSvc.Spec.Template.Annotations,
		},
		Spec: resSvc.Spec.Template.Spec,
	}, nil
}

// calculateLabels merges user-supplied template labels with controller-owned stack
// labels. Stack labels take precedence so ownership cannot be overridden accidentally.
func (m *defaultServiceManager) calculateLabels(resSvc *kubemodel.ServiceResource) map[string]string {
	stackLabels := m.trackingProvider.KubernetesSafeStackLabels(resSvc.Stack())
	if resSvc.Spec.Template.Labels == nil {
		return stackLabels
	}
	return algorithm.MergeStringMap(resSvc.Spec.Template.Labels, stackLabels)
}

func buildObservedServiceStatus(k8sSvc *corev1.Service) kubemodel.ObservedServiceStatus {
	return kubemodel.ObservedServiceStatus{
		ServiceRef: corev1.ObjectReference{
			Namespace: k8sSvc.Namespace,
			Name:      k8sSvc.Name,
			UID:       k8sSvc.UID,
		},
	}
}

func namespacedName(obj metav1.Object) types.NamespacedName {
	return types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
}

// servicePortsEqual returns true when the two ServicePort slices match on all
// controller-owned fields. Cluster-assigned fields (NodePort) are ignored so we
// don't churn the Service when the cluster assigns or keeps a NodePort value.
func servicePortsEqual(existing, desired []corev1.ServicePort) bool {
	if len(existing) != len(desired) {
		return false
	}
	existingByKey := indexServicePorts(existing)
	for _, d := range desired {
		e, ok := existingByKey[servicePortKey(d)]
		if !ok {
			return false
		}
		if e.Name != d.Name ||
			e.Protocol != d.Protocol ||
			e.Port != d.Port ||
			!equality.Semantic.DeepEqual(e.TargetPort, d.TargetPort) ||
			!equality.Semantic.DeepEqual(e.AppProtocol, d.AppProtocol) {
			return false
		}
	}
	return true
}

// mergeServicePorts produces the desired port list while preserving cluster-
// assigned NodePort values from the existing Service so Kubernetes doesn't have
// to re-allocate them on every reconcile.
func mergeServicePorts(existing, desired []corev1.ServicePort) []corev1.ServicePort {
	existingByKey := indexServicePorts(existing)
	merged := make([]corev1.ServicePort, 0, len(desired))
	for _, d := range desired {
		if e, ok := existingByKey[servicePortKey(d)]; ok && d.NodePort == 0 {
			d.NodePort = e.NodePort
		}
		merged = append(merged, d)
	}
	return merged
}

// servicePortKey identifies a ServicePort by (port, protocol) so ports can be
// matched across existing and desired lists regardless of their ordering.
func servicePortKey(p corev1.ServicePort) string {
	protocol := p.Protocol
	if protocol == "" {
		protocol = corev1.ProtocolTCP
	}
	return fmt.Sprintf("%d/%s", p.Port, protocol)
}

func indexServicePorts(ports []corev1.ServicePort) map[string]corev1.ServicePort {
	out := make(map[string]corev1.ServicePort, len(ports))
	for _, p := range ports {
		out[servicePortKey(p)] = p
	}
	return out
}
