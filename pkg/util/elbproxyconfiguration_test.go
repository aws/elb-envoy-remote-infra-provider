// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"testing"
	"time"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestIsGatewayManagedByEnvoyGateway(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1.Install(scheme)

	tests := []struct {
		name         string
		gateway      *gwapiv1.Gateway
		gatewayClass *gwapiv1.GatewayClass
		expected     bool
	}{
		{
			name:     "nil gateway",
			gateway:  nil,
			expected: false,
		},
		{
			name: "matching controller",
			gateway: &gwapiv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gateway",
					Namespace: "default",
				},
				Spec: gwapiv1.GatewaySpec{
					GatewayClassName: "test-class",
				},
			},
			gatewayClass: &gwapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class",
				},
				Spec: gwapiv1.GatewayClassSpec{
					ControllerName: egv1a1.GatewayControllerName,
				},
			},
			expected: true,
		},
		{
			name: "non-matching controller",
			gateway: &gwapiv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gateway",
					Namespace: "default",
				},
				Spec: gwapiv1.GatewaySpec{
					GatewayClassName: "test-class",
				},
			},
			gatewayClass: &gwapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class",
				},
				Spec: gwapiv1.GatewayClassSpec{
					ControllerName: "other-controller",
				},
			},
			expected: false,
		},
		{
			name: "gatewayclass not found",
			gateway: &gwapiv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gateway",
					Namespace: "default",
				},
				Spec: gwapiv1.GatewaySpec{
					GatewayClassName: "non-existent-class",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c client.Client
			if tt.gatewayClass != nil {
				c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.gatewayClass).Build()
			} else {
				c = fake.NewClientBuilder().WithScheme(scheme).Build()
			}

			result := IsGatewayManagedByEnvoyGateway(context.Background(), c, tt.gateway)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func proxyWithTargets(name, namespace string, refs ...gwapiv1a2Ref) *elbproxyv1alpha1.ELBProxyConfiguration {
	out := &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	for _, r := range refs {
		out.Spec.TargetRefs = append(out.Spec.TargetRefs, gwapiv1a2.LocalPolicyTargetReference{
			Group: gwapiv1.Group(r.group),
			Kind:  gwapiv1.Kind(r.kind),
			Name:  gwapiv1.ObjectName(r.name),
		})
	}
	return out
}

type gwapiv1a2Ref struct {
	group, kind, name string
}

var (
	gwRef    = func(name string) gwapiv1a2Ref { return gwapiv1a2Ref{gwapiv1.GroupName, "Gateway", name} }
	classRef = func(name string) gwapiv1a2Ref { return gwapiv1a2Ref{gwapiv1.GroupName, "GatewayClass", name} }
	otherRef = func(name string) gwapiv1a2Ref { return gwapiv1a2Ref{"example.com", "Gateway", name} }
)

func TestResolveEffectiveProxy(t *testing.T) {
	older := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))
	sameTime := metav1.NewTime(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC))

	gatewayScoped := proxyWithTargets("p-gw", "ns", gwRef("gw"))
	classScoped := proxyWithTargets("p-class", "system", classRef("eg"))
	otherClass := proxyWithTargets("p-other-class", "system", classRef("other"))
	crossNS := proxyWithTargets("p-xns", "other", gwRef("gw"))      // Gateway ref in wrong namespace
	wrongGroup := proxyWithTargets("p-wrong", "ns", otherRef("gw")) // non-gwapi group
	unrelatedKind := proxyWithTargets("p-kind", "ns", gwapiv1a2Ref{gwapiv1.GroupName, "Service", "gw"})
	multiRef := proxyWithTargets("p-multi", "system", classRef("eg"), gwRef("gw"))

	classOld := proxyWithTargets("p-old", "system", classRef("eg"))
	classOld.CreationTimestamp = older
	classNew := proxyWithTargets("p-new", "system", classRef("eg"))
	classNew.CreationTimestamp = newer

	// Tier-1 tiebreak: two Gateway-scoped targeting same gw; different creationTimestamps.
	gwOlder := proxyWithTargets("gw-old", "ns", gwRef("gw"))
	gwOlder.CreationTimestamp = older
	gwNewer := proxyWithTargets("gw-new", "ns", gwRef("gw"))
	gwNewer.CreationTimestamp = newer

	// Same timestamp and namespace, different name (name is tertiary key).
	nameA := proxyWithTargets("a-proxy", "system", classRef("eg"))
	nameA.CreationTimestamp = sameTime
	nameB := proxyWithTargets("b-proxy", "system", classRef("eg"))
	nameB.CreationTimestamp = sameTime

	tests := []struct {
		name      string
		proxies   []*elbproxyv1alpha1.ELBProxyConfiguration
		className string
		want      string
	}{
		{"no proxies", nil, "eg", ""},
		{"nil entries skipped", []*elbproxyv1alpha1.ELBProxyConfiguration{nil, classScoped}, "eg", "p-class"},
		{"gateway-scoped wins over class-scoped", []*elbproxyv1alpha1.ELBProxyConfiguration{classScoped, gatewayScoped}, "eg", "p-gw"},
		{"single proxy with both refs — tier-1 still wins", []*elbproxyv1alpha1.ELBProxyConfiguration{multiRef}, "eg", "p-multi"},
		{"class-scoped fallback", []*elbproxyv1alpha1.ELBProxyConfiguration{classScoped}, "eg", "p-class"},
		{"class mismatch — no match", []*elbproxyv1alpha1.ELBProxyConfiguration{otherClass}, "eg", ""},
		{"cross-namespace gateway ref ignored", []*elbproxyv1alpha1.ELBProxyConfiguration{crossNS}, "eg", ""},
		{"wrong group ignored", []*elbproxyv1alpha1.ELBProxyConfiguration{wrongGroup, classScoped}, "eg", "p-class"},
		{"unrelated kind ignored", []*elbproxyv1alpha1.ELBProxyConfiguration{unrelatedKind, classScoped}, "eg", "p-class"},
		{"empty class disables tier 2", []*elbproxyv1alpha1.ELBProxyConfiguration{classScoped}, "", ""},
		{"tier-1 tiebreak by creationTimestamp", []*elbproxyv1alpha1.ELBProxyConfiguration{gwNewer, gwOlder}, "eg", "gw-old"},
		{"tier-2 tiebreak by creationTimestamp", []*elbproxyv1alpha1.ELBProxyConfiguration{classNew, classOld}, "eg", "p-old"},
		{"tie tertiary key — name ascending", []*elbproxyv1alpha1.ELBProxyConfiguration{nameB, nameA}, "eg", "a-proxy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveEffectiveProxy(tt.proxies, "ns", "gw", tt.className, "system")
			if tt.want == "" {
				assert.Nil(t, got)
				return
			}
			assert.NotNil(t, got)
			assert.Equal(t, tt.want, got.Name)
		})
	}
}

func TestClassesReferencedByTargetRefs(t *testing.T) {
	tests := []struct {
		name  string
		proxy *elbproxyv1alpha1.ELBProxyConfiguration
		want  []string
	}{
		{"nil proxy", nil, nil},
		{"no refs", proxyWithTargets("p", "system"), nil},
		{"only gateway refs", proxyWithTargets("p", "system", gwRef("a"), gwRef("b")), nil},
		{"class refs returned", proxyWithTargets("p", "system", classRef("a"), gwRef("b"), classRef("c")), []string{"a", "c"}},
		{"non-gwapi class ref ignored", proxyWithTargets("p", "system", gwapiv1a2Ref{"example.com", "GatewayClass", "x"}, classRef("y")), []string{"y"}},
		{"proxy outside controller namespace — class refs ignored", proxyWithTargets("p", "tenant", classRef("a")), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassesReferencedByTargetRefs(tt.proxy, "system"))
		})
	}
}

func TestGatewaysReferencedByTargetRefs(t *testing.T) {
	tests := []struct {
		name  string
		proxy *elbproxyv1alpha1.ELBProxyConfiguration
		want  []string
	}{
		{"nil proxy", nil, nil},
		{"no refs", proxyWithTargets("p", "ns"), nil},
		{"only class refs", proxyWithTargets("p", "ns", classRef("a")), nil},
		{"gateway refs prefixed with proxy namespace", proxyWithTargets("p", "app", gwRef("gw1"), classRef("eg"), gwRef("gw2")), []string{"app/gw1", "app/gw2"}},
		{"wrong group ignored", proxyWithTargets("p", "app", gwapiv1a2Ref{"example.com", "Gateway", "gw1"}, gwRef("gw2")), []string{"app/gw2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GatewaysReferencedByTargetRefs(tt.proxy))
		})
	}
}

func TestClassReferencedByTargetRefs(t *testing.T) {
	p := proxyWithTargets("p", "system", classRef("eg"), gwRef("gw"))
	wrongGroupClass := proxyWithTargets("pw", "system", gwapiv1a2Ref{"example.com", "GatewayClass", "eg"})
	wrongNS := proxyWithTargets("p", "tenant", classRef("eg"))
	tests := []struct {
		name      string
		proxy     *elbproxyv1alpha1.ELBProxyConfiguration
		className string
		want      bool
	}{
		{"nil proxy", nil, "eg", false},
		{"empty className", p, "", false},
		{"match", p, "eg", true},
		{"no match", p, "other", false},
		{"wrong group ignored", wrongGroupClass, "eg", false},
		{"proxy outside controller namespace ignored", wrongNS, "eg", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassReferencedByTargetRefs(tt.proxy, tt.className, "system"))
		})
	}
}

func TestIsMergedGatewayKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"merged-eg", true},
		{"eg", true},
		{"default/my-gateway", false},
		{"ns/gw", false},
		{"", true}, // empty string has no slash — treated as merged
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			assert.Equal(t, tt.want, IsMergedGatewayKey(tt.key))
		})
	}
}

func TestResolveEffectiveProxy_MergedGateway(t *testing.T) {
	classScoped := proxyWithTargets("p-class", "system", classRef("eg"))
	gatewayScoped := proxyWithTargets("p-gw", "envoy-gateway-system", gwRef("eg")) // targets the bare class name as a Gateway — should be ignored

	tests := []struct {
		name    string
		proxies []*elbproxyv1alpha1.ELBProxyConfiguration
		want    string
	}{
		{
			name:    "merged: class-scoped proxy wins",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{classScoped},
			want:    "p-class",
		},
		{
			name:    "merged: gateway-scoped proxy ignored, class-scoped wins",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{gatewayScoped, classScoped},
			want:    "p-class",
		},
		{
			name:    "merged: only gateway-scoped proxy — no match",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{gatewayScoped},
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Merged gateway: empty namespace signals merged fleet to the resolver.
			got := ResolveEffectiveProxy(tt.proxies, "", "eg", "eg", "system")
			if tt.want == "" {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tt.want, got.Name)
			}
		})
	}
}

func TestResolveEffectiveProxyWithLosers(t *testing.T) {
	older := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))

	gwWinner := proxyWithTargets("gw-old", "ns", gwRef("gw"))
	gwWinner.CreationTimestamp = older
	gwLoser := proxyWithTargets("gw-new", "ns", gwRef("gw"))
	gwLoser.CreationTimestamp = newer

	classWinner := proxyWithTargets("cls-old", "system", classRef("eg"))
	classWinner.CreationTimestamp = older
	classLoser := proxyWithTargets("cls-new", "system", classRef("eg"))
	classLoser.CreationTimestamp = newer

	tests := []struct {
		name       string
		proxies    []*elbproxyv1alpha1.ELBProxyConfiguration
		wantWinner string
		wantLosers []string
	}{
		{"no matches", nil, "", nil},
		{"single winner no losers", []*elbproxyv1alpha1.ELBProxyConfiguration{gwWinner}, "gw-old", nil},
		{"tier-1 tie — losers returned", []*elbproxyv1alpha1.ELBProxyConfiguration{gwLoser, gwWinner}, "gw-old", []string{"gw-new"}},
		{"tier-2 tie — losers returned", []*elbproxyv1alpha1.ELBProxyConfiguration{classLoser, classWinner}, "cls-old", []string{"cls-new"}},
		{"tier-1 override — tier-2 matches not reported as losers", []*elbproxyv1alpha1.ELBProxyConfiguration{gwWinner, classWinner}, "gw-old", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, losers := ResolveEffectiveProxyWithLosers(tt.proxies, "ns", "gw", "eg", "system")
			if tt.wantWinner == "" {
				assert.Nil(t, w)
			} else {
				require.NotNil(t, w)
				assert.Equal(t, tt.wantWinner, w.Name)
			}
			var loserNames []string
			for _, l := range losers {
				loserNames = append(loserNames, l.Name)
			}
			assert.Equal(t, tt.wantLosers, loserNames)
		})
	}
}

// TestResolveEffectiveProxy_ClassScoped_NamespaceGated verifies that GatewayClass-scoped
// targetRefs are only honored when the ELBProxyConfiguration lives in the controller's own
// namespace (ENVOY_GATEWAY_NAMESPACE). This is the multi-tenant safety guarantee:
// each tenant deploys their own Envoy Gateway controller in their own namespace,
// and an ELBProxyConfiguration in tenant A's namespace must not attach to a GatewayClass
// managed by tenant B's controller.
func TestResolveEffectiveProxy_ClassScoped_NamespaceGated(t *testing.T) {
	tests := []struct {
		name           string
		controllerNS   string
		proxies        []*elbproxyv1alpha1.ELBProxyConfiguration
		wantWinnerName string
	}{
		{
			name:         "class-scoped proxy in controller namespace — honored",
			controllerNS: "tenant-a-system",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				proxyWithTargets("p", "tenant-a-system", classRef("eg")),
			},
			wantWinnerName: "p",
		},
		{
			name:         "class-scoped proxy in tenant namespace — ignored",
			controllerNS: "tenant-a-system",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				proxyWithTargets("p", "tenant-b", classRef("eg")),
			},
			wantWinnerName: "",
		},
		{
			name:         "default ENVOY_GATEWAY_NAMESPACE applies",
			controllerNS: "", // unset — falls back to "envoy-gateway-system"
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				proxyWithTargets("p", "envoy-gateway-system", classRef("eg")),
			},
			wantWinnerName: "p",
		},
		{
			name:         "Gateway-scoped attachment unaffected by namespace gating",
			controllerNS: "tenant-a-system",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				// Tenant lives in their own namespace — Gateway-scoped attachment must still work.
				proxyWithTargets("p", "ns", gwRef("gw")),
			},
			wantWinnerName: "p",
		},
		{
			name:         "mix: in-namespace class-scoped wins, out-of-namespace ignored",
			controllerNS: "tenant-a-system",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				proxyWithTargets("p-bad", "tenant-b", classRef("eg")),
				proxyWithTargets("p-good", "tenant-a-system", classRef("eg")),
			},
			wantWinnerName: "p-good",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controllerNS := tt.controllerNS
			if controllerNS == "" {
				controllerNS = defaultEnvoyGatewayNamespace
			}
			got := ResolveEffectiveProxy(tt.proxies, "ns", "gw", "eg", controllerNS)
			if tt.wantWinnerName == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantWinnerName, got.Name)
		})
	}
}

// TestResolveEffectiveProxy_ClassScoped_NamespaceGated_MergedGateway verifies the
// same multi-tenant gating for merged-gateway resolution (where Gateway-scoped
// refs are ignored regardless).
func TestResolveEffectiveProxy_ClassScoped_NamespaceGated_MergedGateway(t *testing.T) {
	inNS := proxyWithTargets("p-in", "tenant-a-system", classRef("merged-eg"))
	outOfNS := proxyWithTargets("p-out", "tenant-b", classRef("merged-eg"))

	// Merged gateway: empty namespace signals merged fleet.
	t.Run("class-scoped in controller namespace wins", func(t *testing.T) {
		got := ResolveEffectiveProxy([]*elbproxyv1alpha1.ELBProxyConfiguration{inNS}, "", "merged-eg", "merged-eg", "tenant-a-system")
		require.NotNil(t, got)
		assert.Equal(t, "p-in", got.Name)
	})
	t.Run("class-scoped outside controller namespace ignored", func(t *testing.T) {
		got := ResolveEffectiveProxy([]*elbproxyv1alpha1.ELBProxyConfiguration{outOfNS}, "", "merged-eg", "merged-eg", "tenant-a-system")
		assert.Nil(t, got)
	})
}
