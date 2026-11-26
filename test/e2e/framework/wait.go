// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
)

// contextWithTimeout derives a child context from the test, bounded by d.
func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(t.Context())
	}
	return context.WithTimeout(t.Context(), d)
}

// waitHeartbeat returns a closure that logs a progress line at most every ~15s,
// so a long poll shows liveness. Call it each iteration with the current status.
func waitHeartbeat(t *testing.T, what string, timeout time.Duration) func(detail string) {
	t.Helper()
	start := time.Now()
	Logf(t, "waiting for %s (up to %s)", what, timeout)
	var lastLog time.Time
	return func(detail string) {
		now := time.Now()
		if now.Sub(lastLog) < 15*time.Second {
			return
		}
		lastLog = now
		if detail == "" {
			Logf(t, "  still waiting for %s (%s elapsed)", what, time.Since(start).Round(time.Second))
		} else {
			Logf(t, "  still waiting for %s (%s elapsed): %s", what, time.Since(start).Round(time.Second), detail)
		}
	}
}

// WaitForGatewayAccepted polls until the Gateway is Accepted+Programmed with an
// address, which it returns.
func (s *Suite) WaitForGatewayAccepted(t *testing.T, gwNN types.NamespacedName) string {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.Address)
	defer cancel()

	beat := waitHeartbeat(t, "Gateway "+gwNN.String()+" to be Programmed with an address", s.Timeouts.Address)
	var address string
	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.Address, true, func(ctx context.Context) (bool, error) {
		gw := &gwapiv1.Gateway{}
		if err := s.Client.Get(ctx, gwNN, gw); err != nil {
			beat("Gateway not found yet")
			return false, nil
		}
		if !hasCondition(gw.Status.Conditions, string(gwapiv1.GatewayConditionAccepted), metav1.ConditionTrue) {
			beat("Accepted!=True")
			return false, nil
		}
		if !hasCondition(gw.Status.Conditions, string(gwapiv1.GatewayConditionProgrammed), metav1.ConditionTrue) {
			beat("Programmed!=True")
			return false, nil
		}
		for _, addr := range gw.Status.Addresses {
			if addr.Value != "" {
				address = addr.Value
				return true, nil
			}
		}
		beat("Programmed but no address assigned yet")
		return false, nil
	})
	if err != nil {
		t.Fatalf("Gateway %s did not become Accepted+Programmed with an address: %v", gwNN, err)
	}
	Logf(t, "Gateway %s is Programmed with address %s", gwNN, address)
	return address
}

// WaitForHTTPRouteAccepted polls until the HTTPRoute is Accepted+ResolvedRefs
// against parentGwNN.
func (s *Suite) WaitForHTTPRouteAccepted(t *testing.T, routeNN, parentGwNN types.NamespacedName) {
	t.Helper()
	s.waitForRouteAccepted(t, routeNN, parentGwNN, "HTTPRoute", &gwapiv1.HTTPRoute{}, func(obj client.Object) []gwapiv1.RouteParentStatus {
		return obj.(*gwapiv1.HTTPRoute).Status.Parents
	})
}

// WaitForListenerSetAccepted polls until the ListenerSet is Accepted.
func (s *Suite) WaitForListenerSetAccepted(t *testing.T, lsNN types.NamespacedName) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.Accepted)
	defer cancel()
	beat := waitHeartbeat(t, "ListenerSet "+lsNN.String()+" to be Accepted", s.Timeouts.Accepted)
	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.Accepted, true, func(ctx context.Context) (bool, error) {
		ls := &gwapiv1.ListenerSet{}
		if err := s.Client.Get(ctx, lsNN, ls); err != nil {
			beat("ListenerSet not found yet")
			return false, nil
		}
		if !hasCondition(ls.Status.Conditions, string(gwapiv1.ListenerSetConditionAccepted), metav1.ConditionTrue) {
			beat("Accepted!=True")
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("ListenerSet %s did not become Accepted: %v", lsNN, err)
	}
	Logf(t, "ListenerSet %s is Accepted", lsNN)
}

// WaitForELBProxyConfigurationAccepted polls until the ELBProxyConfiguration reports Accepted=True.
func (s *Suite) WaitForELBProxyConfigurationAccepted(t *testing.T, ppNN types.NamespacedName) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.Accepted)
	defer cancel()

	beat := waitHeartbeat(t, "ELBProxyConfiguration "+ppNN.String()+" to be Accepted", s.Timeouts.Accepted)
	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.Accepted, true, func(ctx context.Context) (bool, error) {
		pp := &elbproxyv1alpha1.ELBProxyConfiguration{}
		if err := s.Client.Get(ctx, ppNN, pp); err != nil {
			beat("ELBProxyConfiguration not found yet")
			return false, nil
		}
		ok := hasCondition(pp.Status.Conditions, "Accepted", metav1.ConditionTrue)
		if !ok {
			beat("Accepted!=True")
		}
		return ok, nil
	})
	if err != nil {
		t.Fatalf("ELBProxyConfiguration %s did not become Accepted: %v", ppNN, err)
	}
	Logf(t, "ELBProxyConfiguration %s is Accepted", ppNN)
}

// WaitForELBProxyConfigurationConditionReason polls until the ELBProxyConfiguration reports the
// given condition type with the given status and reason. Use it to assert
// non-Accepted outcomes, e.g. Accepted=False/Conflicted on a tiebreak loser.
func (s *Suite) WaitForELBProxyConfigurationConditionReason(t *testing.T, ppNN types.NamespacedName, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.Accepted)
	defer cancel()

	what := ppNN.String() + " " + condType + "=" + string(status) + "/" + reason
	beat := waitHeartbeat(t, "ELBProxyConfiguration "+what, s.Timeouts.Accepted)
	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.Accepted, true, func(ctx context.Context) (bool, error) {
		pp := &elbproxyv1alpha1.ELBProxyConfiguration{}
		if err := s.Client.Get(ctx, ppNN, pp); err != nil {
			beat("ELBProxyConfiguration not found yet")
			return false, nil
		}
		c := apimeta.FindStatusCondition(pp.Status.Conditions, condType)
		if c == nil {
			beat(condType + " condition absent")
			return false, nil
		}
		if c.Status != status || c.Reason != reason {
			beat(condType + "=" + string(c.Status) + "/" + c.Reason)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("ELBProxyConfiguration %s did not reach %s: %v", ppNN, what, err)
	}
	Logf(t, "ELBProxyConfiguration %s reached %s", ppNN, what)
}

// WaitForServiceEndpoints polls until the Service has a LoadBalancer ingress
// and returns its hostname or IP.
func (s *Suite) WaitForServiceEndpoints(t *testing.T, svcNN types.NamespacedName) string {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.Address)
	defer cancel()

	var host string
	beat := waitHeartbeat(t, "Service "+svcNN.String()+" to get a LoadBalancer ingress", s.Timeouts.Address)
	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.Address, true, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := s.Client.Get(ctx, svcNN, svc); err != nil {
			beat("Service not found yet")
			return false, nil
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.Hostname != "" {
				host = ing.Hostname
				return true, nil
			}
			if ing.IP != "" {
				host = ing.IP
				return true, nil
			}
		}
		beat("no LoadBalancer ingress assigned yet")
		return false, nil
	})
	if err != nil {
		t.Fatalf("Service %s did not get a LoadBalancer ingress: %v", svcNN, err)
	}
	Logf(t, "Service %s has LoadBalancer ingress %s", svcNN, host)
	return host
}

// hasCondition reports whether conds has type t with the given status.
func hasCondition(conds []metav1.Condition, t string, status metav1.ConditionStatus) bool {
	for _, c := range conds {
		if c.Type == t && c.Status == status {
			return true
		}
	}
	return false
}

// parentReports reports whether a parent referencing gwNN has the given
// condition status.
func parentReports(parents []gwapiv1.RouteParentStatus, gwNN types.NamespacedName, condType string, status metav1.ConditionStatus) bool {
	for _, p := range parents {
		if !parentMatches(p.ParentRef, gwNN) {
			continue
		}
		if hasCondition(p.Conditions, condType, status) {
			return true
		}
	}
	return false
}

// parentMatches reports whether ref points at gwNN (Gateway, matching name and
// namespace; empty parentRef namespace is treated as same-namespace).
func parentMatches(ref gwapiv1.ParentReference, gwNN types.NamespacedName) bool {
	if ref.Group != nil && string(*ref.Group) != gwapiv1.GroupName {
		return false
	}
	if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
		return false
	}
	if string(ref.Name) != gwNN.Name {
		return false
	}
	if ref.Namespace != nil && string(*ref.Namespace) != "" {
		return string(*ref.Namespace) == gwNN.Namespace
	}
	return true
}

// LookupClusterIP returns a Service's ClusterIP.
func (s *Suite) LookupClusterIP(t *testing.T, svcNN types.NamespacedName) string {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, 30*time.Second)
	defer cancel()

	svc := &corev1.Service{}
	if err := s.Client.Get(ctx, svcNN, svc); err != nil {
		t.Fatalf("get Service %s: %v", svcNN, err)
	}
	if svc.Spec.ClusterIP == "" {
		t.Fatalf("Service %s has no ClusterIP", svcNN)
	}
	return svc.Spec.ClusterIP
}

// WaitForTCPRouteAccepted polls until the TCPRoute is Accepted+ResolvedRefs
// against parentGwNN.
func (s *Suite) WaitForTCPRouteAccepted(t *testing.T, routeNN, parentGwNN types.NamespacedName) {
	t.Helper()
	s.waitForRouteAccepted(t, routeNN, parentGwNN, "TCPRoute", &gwapiv1.TCPRoute{}, func(obj client.Object) []gwapiv1.RouteParentStatus {
		return obj.(*gwapiv1.TCPRoute).Status.Parents
	})
}

// WaitForGRPCRouteAccepted polls until the GRPCRoute is Accepted+ResolvedRefs
// against parentGwNN.
func (s *Suite) WaitForGRPCRouteAccepted(t *testing.T, routeNN, parentGwNN types.NamespacedName) {
	t.Helper()
	s.waitForRouteAccepted(t, routeNN, parentGwNN, "GRPCRoute", &gwapiv1.GRPCRoute{}, func(obj client.Object) []gwapiv1.RouteParentStatus {
		return obj.(*gwapiv1.GRPCRoute).Status.Parents
	})
}

// WaitForTLSRouteAccepted polls until the TLSRoute is Accepted+ResolvedRefs
// against parentGwNN.
func (s *Suite) WaitForTLSRouteAccepted(t *testing.T, routeNN, parentGwNN types.NamespacedName) {
	t.Helper()
	s.waitForRouteAccepted(t, routeNN, parentGwNN, "TLSRoute", &gwapiv1.TLSRoute{}, func(obj client.Object) []gwapiv1.RouteParentStatus {
		return obj.(*gwapiv1.TLSRoute).Status.Parents
	})
}

// WaitForUDPRouteAccepted polls until the UDPRoute is Accepted+ResolvedRefs
// against parentGwNN.
func (s *Suite) WaitForUDPRouteAccepted(t *testing.T, routeNN, parentGwNN types.NamespacedName) {
	t.Helper()
	s.waitForRouteAccepted(t, routeNN, parentGwNN, "UDPRoute", &gwapiv1.UDPRoute{}, func(obj client.Object) []gwapiv1.RouteParentStatus {
		return obj.(*gwapiv1.UDPRoute).Status.Parents
	})
}

// waitForRouteAccepted is the shared implementation for all route types.
func (s *Suite) waitForRouteAccepted(t *testing.T, routeNN, parentGwNN types.NamespacedName, kind string, obj client.Object, getParents func(client.Object) []gwapiv1.RouteParentStatus) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.Accepted)
	defer cancel()

	beat := waitHeartbeat(t, kind+" "+routeNN.String()+" to be Accepted+ResolvedRefs", s.Timeouts.Accepted)
	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.Accepted, true, func(ctx context.Context) (bool, error) {
		if err := s.Client.Get(ctx, routeNN, obj); err != nil {
			beat(kind + " not found yet")
			return false, nil
		}
		parents := getParents(obj)
		ok := parentReports(parents, parentGwNN, string(gwapiv1.RouteConditionAccepted), metav1.ConditionTrue) &&
			parentReports(parents, parentGwNN, string(gwapiv1.RouteConditionResolvedRefs), metav1.ConditionTrue)
		if !ok {
			beat("parent conditions not all True yet")
		}
		return ok, nil
	})
	if err != nil {
		t.Fatalf("%s %s did not become Accepted+ResolvedRefs against %s: %v", kind, routeNN, parentGwNN, err)
	}
	Logf(t, "%s %s is Accepted+ResolvedRefs against %s", kind, routeNN, parentGwNN)
}

// WaitForReconcile polls until the ELBProxyConfiguration's Accepted condition has
// observedGeneration matching metadata.generation, indicating the controller
// has processed the latest spec change. This ensures attribute updates have
// been reconciled to ELBProxy before test cleanup triggers deletion.
func (s *Suite) WaitForReconcile(t *testing.T, ppNN types.NamespacedName) {
	t.Helper()
	timeout := 30 * time.Second
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()

	Logf(t, "waiting for ELBProxyConfiguration %s reconcile to catch up (up to %s)", ppNN, timeout)
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pp := &elbproxyv1alpha1.ELBProxyConfiguration{}
		if err := s.Client.Get(ctx, ppNN, pp); err != nil {
			return false, nil
		}
		c := apimeta.FindStatusCondition(pp.Status.Conditions, "Accepted")
		if c == nil {
			return false, nil
		}
		return c.ObservedGeneration >= pp.Generation, nil
	})
	if err != nil {
		t.Logf("warning: ELBProxyConfiguration %s reconcile did not catch up within %s", ppNN, timeout)
	}
}
