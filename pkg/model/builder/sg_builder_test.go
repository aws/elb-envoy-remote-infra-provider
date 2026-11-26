// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	ec2model "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
)

func testBuilderLogger() logging.Logger {
	return logging.Logger{Logger: logr.New(&log.NullLogSink{})}
}

func TestHasUserProvidedSecurityGroups(t *testing.T) {
	tests := []struct {
		name           string
		elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration
		want           bool
	}{
		{
			name:           "no security groups",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{},
			want:           false,
		},
		{
			name: "with user-provided security groups",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						VpcConfiguration: elbproxyv1alpha1.ProxyVpcUserConfiguration{
							SecurityGroups: []elbproxyv1alpha1.SecurityGroupDetails{
								{SecurityGroupId: "sg-123"},
							},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasUserProvidedSecurityGroups(tt.elbProxyConfig)
			if got != tt.want {
				t.Errorf("hasUserProvidedSecurityGroups() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildManagedSecurityGroup(t *testing.T) {
	irInfra := &pb.Infra{
		Proxy: &pb.ProxyInfra{
			Name: "default/my-gateway",
			Listeners: []*pb.ProxyListener{
				{
					Ports: []*pb.ListenerPort{
						{ServicePort: 80},
						{ServicePort: 443},
					},
				},
			},
		},
	}
	elbProxyConfig := &elbproxyv1alpha1.ELBProxyConfiguration{}
	stack := core.NewDefaultStack(core.StackID{Namespace: "default", Name: "test-stack"})

	b := &securityGroupBuilder{}
	sg, err := b.buildManagedSecurityGroup(stack, ir.New(irInfra), elbProxyConfig, "off")
	if err != nil {
		t.Fatalf("buildManagedSecurityGroup() unexpected error: %v", err)
	}
	if sg == nil {
		t.Fatal("buildManagedSecurityGroup() returned nil")
	}

	wantGroupName := buildManagedSecurityGroupName("", "default", "my-gateway")
	if sg.Spec.GroupName != wantGroupName {
		t.Errorf("GroupName = %q, want %q", sg.Spec.GroupName, wantGroupName)
	}
	if len(sg.Spec.Ingress) != 2 {
		t.Errorf("Ingress count = %d, want 2", len(sg.Spec.Ingress))
	}

	var sgResources []*ec2model.SecurityGroup
	if err := stack.ListResources(&sgResources); err != nil {
		t.Fatalf("ListResources() error: %v", err)
	}
	if len(sgResources) != 1 {
		t.Errorf("stack has %d SG resources, want 1", len(sgResources))
	}
}

func TestBuildManagedSecurityGroupIngressPermissions(t *testing.T) {
	type wantRule struct {
		port     int32
		protocol string
	}
	tests := []struct {
		name  string
		infra *pb.Infra
		want  []wantRule
	}{
		{
			name: "one port",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{{ServicePort: 80}}},
					},
				},
			},
			want: []wantRule{{80, "tcp"}},
		},
		{
			name: "multiple ports",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{
							{ServicePort: 80, Protocol: ir.HTTPProtocolType},
							{ServicePort: 443, Protocol: ir.HTTPSProtocolType},
						}},
					},
				},
			},
			want: []wantRule{{80, "tcp"}, {443, "tcp"}},
		},
		{
			name: "duplicate ports are deduplicated",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{{ServicePort: 80, Protocol: ir.HTTPProtocolType}}},
						{Ports: []*pb.ListenerPort{{ServicePort: 80, Protocol: ir.HTTPProtocolType}}},
					},
				},
			},
			want: []wantRule{{80, "tcp"}},
		},
		{
			name: "no listeners",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name:      "default/gw",
					Listeners: []*pb.ProxyListener{},
				},
			},
			want: nil,
		},
		{
			name: "UDP listener uses udp protocol",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{{ServicePort: 53, Protocol: ir.UDPProtocolType}}},
					},
				},
			},
			want: []wantRule{{53, "udp"}},
		},
		{
			name: "HTTP protocol maps to tcp",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{{ServicePort: 80, Protocol: ir.HTTPProtocolType}}},
					},
				},
			},
			want: []wantRule{{80, "tcp"}},
		},
		{
			name: "HTTPS protocol maps to tcp",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.HTTPSProtocolType}}},
					},
				},
			},
			want: []wantRule{{443, "tcp"}},
		},
		{
			name: "TLS protocol maps to tcp",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{Ports: []*pb.ListenerPort{{ServicePort: 8443, Protocol: ir.TLSProtocolType}}},
					},
				},
			},
			want: []wantRule{{8443, "tcp"}},
		},
		{
			name: "HTTP/3 listener opens udp next to tcp on its HTTPS port",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{
							Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.HTTPSProtocolType}},
							Http3: &pb.HTTP3Settings{},
						},
					},
				},
			},
			want: []wantRule{{443, "tcp"}, {443, "udp"}},
		},
		{
			name: "HTTP/3 opens udp only on HTTPS ports",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{
							Ports: []*pb.ListenerPort{
								{ServicePort: 80, Protocol: ir.HTTPProtocolType},
								{ServicePort: 443, Protocol: ir.HTTPSProtocolType},
							},
							Http3: &pb.HTTP3Settings{},
						},
					},
				},
			},
			want: []wantRule{{80, "tcp"}, {443, "tcp"}, {443, "udp"}},
		},
		{
			name: "HTTP/3 udp rule is deduplicated with a UDP listener on the same port",
			infra: &pb.Infra{
				Proxy: &pb.ProxyInfra{
					Name: "default/gw",
					Listeners: []*pb.ProxyListener{
						{
							Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.HTTPSProtocolType}},
							Http3: &pb.HTTP3Settings{},
						},
						{Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.UDPProtocolType}}},
					},
				},
			},
			want: []wantRule{{443, "tcp"}, {443, "udp"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, err := buildManagedSecurityGroupIngressPermissions(ir.New(tt.infra), &elbproxyv1alpha1.ELBProxyConfiguration{}, "off")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rules) != len(tt.want) {
				t.Fatalf("ingress rules count = %d, want %d", len(rules), len(tt.want))
			}
			for i, w := range tt.want {
				if *rules[i].FromPort != w.port || *rules[i].ToPort != w.port {
					t.Errorf("rule[%d] port = %d-%d, want %d", i, *rules[i].FromPort, *rules[i].ToPort, w.port)
				}
				if rules[i].IPProtocol != w.protocol {
					t.Errorf("rule[%d] protocol = %q, want %q", i, rules[i].IPProtocol, w.protocol)
				}
			}
		})
	}
}

func TestBuildManagedSecurityGroupName(t *testing.T) {
	tests := []struct {
		cluster   string
		namespace string
		name      string
	}{
		{"cluster-a", "default", "my-gateway"},
		{"cluster-a", "namespace", "another-gateway"},
		{"", "default", "my-gateway"},
	}

	for _, tt := range tests {
		t.Run(tt.cluster+"/"+tt.namespace+"/"+tt.name, func(t *testing.T) {
			name := buildManagedSecurityGroupName(tt.cluster, tt.namespace, tt.name)
			// Deterministic.
			if name != buildManagedSecurityGroupName(tt.cluster, tt.namespace, tt.name) {
				t.Error("buildManagedSecurityGroupName is not deterministic")
			}
			// Starts with elbproxy-.
			if !strings.HasPrefix(name, "elbproxy-") {
				t.Errorf("name %q does not start with elbproxy-", name)
			}
			// AWS SG name limit is 255.
			if len(name) > 255 {
				t.Errorf("name length %d exceeds AWS limit of 255", len(name))
			}
		})
	}

	t.Run("distinct names across clusters", func(t *testing.T) {
		a := buildManagedSecurityGroupName("cluster-a", "default", "my-gateway")
		b := buildManagedSecurityGroupName("cluster-b", "default", "my-gateway")
		if a == b {
			t.Errorf("expected different SG names for different clusters, both = %q", a)
		}
	})
}

func TestReconcileNodeSGRules(t *testing.T) {
	const backendSGID = "sg-backend"
	backendPorts := []ir.PortProtocol{{Port: 8080, Protocol: "TCP"}}

	tests := []struct {
		name            string
		setupResolver   func(*networking.MockNodeSGResolver)
		setupReconciler func(*networking.MockSecurityGroupReconciler)
		wantErrMsg      string
	}{
		{
			name: "single node SG reconciled",
			setupResolver: func(m *networking.MockNodeSGResolver) {
				m.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-node"}, nil)
			},
			setupReconciler: func(m *networking.MockSecurityGroupReconciler) {
				m.EXPECT().ReconcileIngress(gomock.Any(), "sg-node", gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name: "multiple node SGs each reconciled",
			setupResolver: func(m *networking.MockNodeSGResolver) {
				m.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-a", "sg-b"}, nil)
			},
			setupReconciler: func(m *networking.MockSecurityGroupReconciler) {
				m.EXPECT().ReconcileIngress(gomock.Any(), "sg-a", gomock.Any(), gomock.Any()).Return(nil)
				m.EXPECT().ReconcileIngress(gomock.Any(), "sg-b", gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name: "resolver error propagates",
			setupResolver: func(m *networking.MockNodeSGResolver) {
				m.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return(nil, errors.New("no ready nodes"))
			},
			wantErrMsg: "failed to resolve node security group IDs",
		},
		{
			name: "reconciler error on one SG propagates",
			setupResolver: func(m *networking.MockNodeSGResolver) {
				m.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-node"}, nil)
			},
			setupReconciler: func(m *networking.MockSecurityGroupReconciler) {
				m.EXPECT().ReconcileIngress(gomock.Any(), "sg-node", gomock.Any(), gomock.Any()).Return(errors.New("throttled"))
			},
			wantErrMsg: "failed to reconcile node security group rules for sg-node",
		},
		{
			name: "reconciler error on one SG does not block the other",
			setupResolver: func(m *networking.MockNodeSGResolver) {
				m.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-a", "sg-b"}, nil)
			},
			setupReconciler: func(m *networking.MockSecurityGroupReconciler) {
				m.EXPECT().ReconcileIngress(gomock.Any(), "sg-a", gomock.Any(), gomock.Any()).Return(errors.New("throttled"))
				m.EXPECT().ReconcileIngress(gomock.Any(), "sg-b", gomock.Any(), gomock.Any()).Return(nil)
			},
			wantErrMsg: "failed to reconcile node security group rules for sg-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockResolver := networking.NewMockNodeSGResolver(ctrl)
			tt.setupResolver(mockResolver)

			mockReconciler := networking.NewMockSecurityGroupReconciler(ctrl)
			if tt.setupReconciler != nil {
				tt.setupReconciler(mockReconciler)
			}

			b := &securityGroupBuilder{
				nodeSGResolver:      mockResolver,
				sgReconciler:        mockReconciler,
				controllerNamespace: "envoy-gateway-system",
				logger:              testBuilderLogger(),
			}

			err := b.reconcileNodeSGRules(context.Background(), backendSGID, backendPorts)
			if tt.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("want error containing %q, got %v", tt.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// The node SG must open Envoy Gateway's xDS and Wasm module server ports from the backend
// SG next to the backend ports: the proxy downloads Wasm modules from the Envoy Gateway
// pods through wasm_cluster (pkg/bootstrap/bootstrap.yaml.tpl).
func TestReconcileNodeSGRules_OpensControlPlanePorts(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockResolver := networking.NewMockNodeSGResolver(ctrl)
	mockResolver.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-node"}, nil)

	var got []networking.IPPermissionInfo
	mockReconciler := networking.NewMockSecurityGroupReconciler(ctrl)
	mockReconciler.EXPECT().ReconcileIngress(gomock.Any(), "sg-node", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, perms []networking.IPPermissionInfo, _ ...networking.SecurityGroupReconcileOption) error {
			got = perms
			return nil
		})

	b := &securityGroupBuilder{
		nodeSGResolver:      mockResolver,
		sgReconciler:        mockReconciler,
		controllerNamespace: "envoy-gateway-system",
		logger:              testBuilderLogger(),
	}
	err := b.reconcileNodeSGRules(context.Background(), "sg-backend", []ir.PortProtocol{{Port: 8080, Protocol: "tcp"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"tcp/8080 from sg-backend", "tcp/18000 from sg-backend", "tcp/18002 from sg-backend"},
		describePermissions(got))
}

// describePermissions renders each ingress permission as "protocol/port from source-SG".
func describePermissions(perms []networking.IPPermissionInfo) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		var source string
		if pairs := p.Permission.UserIdGroupPairs; len(pairs) > 0 && pairs[0].GroupId != nil {
			source = *pairs[0].GroupId
		}
		out = append(out, fmt.Sprintf("%s/%d from %s", *p.Permission.IpProtocol, *p.Permission.FromPort, source))
	}
	return out
}

func TestResolveIngressCIDRs_InvalidCIDR(t *testing.T) {
	proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			IngressCidrs: []string{"10.0.0.0/8", "not-a-cidr"},
		},
	}
	_, _, err := resolveIngressCIDRs(proxy, "off")
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-cidr") {
		t.Errorf("error should mention the invalid CIDR, got: %v", err)
	}
}

func TestBuildManagedSecurityGroupIngressPermissions_PrefixLists(t *testing.T) {
	infra := &pb.Infra{
		Proxy: &pb.ProxyInfra{
			Name: "default/gw",
			Listeners: []*pb.ProxyListener{
				{Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.HTTPSProtocolType}}},
			},
		},
	}

	tests := []struct {
		name          string
		proxy         *elbproxyv1alpha1.ELBProxyConfiguration
		wantRuleCount int
		checkRules    func(t *testing.T, rules []ec2model.IPPermission)
	}{
		{
			name: "single prefix list produces one rule",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressPrefixLists: []string{"pl-0123456789abcdef0"},
					// no ingressCidrs → default 0.0.0.0/0 still applies
				},
			},
			wantRuleCount: 2, // one IPv4 default + one prefix list
			checkRules: func(t *testing.T, rules []ec2model.IPPermission) {
				var plRules []ec2model.IPPermission
				for _, r := range rules {
					if len(r.PrefixLists) > 0 {
						plRules = append(plRules, r)
					}
				}
				if len(plRules) != 1 {
					t.Fatalf("expected 1 prefix list rule, got %d", len(plRules))
				}
				if plRules[0].PrefixLists[0].ListID != "pl-0123456789abcdef0" {
					t.Errorf("prefix list ID = %q, want pl-0123456789abcdef0", plRules[0].PrefixLists[0].ListID)
				}
				if *plRules[0].FromPort != 443 {
					t.Errorf("prefix list rule port = %d, want 443", *plRules[0].FromPort)
				}
			},
		},
		{
			name: "multiple prefix lists produce one rule each",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressPrefixLists: []string{"pl-aaa", "pl-bbb"},
				},
			},
			wantRuleCount: 3, // one IPv4 default + two prefix lists
			checkRules: func(t *testing.T, rules []ec2model.IPPermission) {
				var plIDs []string
				for _, r := range rules {
					if len(r.PrefixLists) > 0 {
						plIDs = append(plIDs, r.PrefixLists[0].ListID)
					}
				}
				if len(plIDs) != 2 {
					t.Fatalf("expected 2 prefix list rules, got %d", len(plIDs))
				}
			},
		},
		{
			name: "prefix list combined with custom CIDR",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs:       []string{"10.0.0.0/8"},
					IngressPrefixLists: []string{"pl-0123456789abcdef0"},
				},
			},
			wantRuleCount: 2, // one custom IPv4 CIDR + one prefix list
			checkRules: func(t *testing.T, rules []ec2model.IPPermission) {
				var cidrRules, plRules []ec2model.IPPermission
				for _, r := range rules {
					if len(r.IPRanges) > 0 {
						cidrRules = append(cidrRules, r)
					}
					if len(r.PrefixLists) > 0 {
						plRules = append(plRules, r)
					}
				}
				if len(cidrRules) != 1 || cidrRules[0].IPRanges[0].CIDRIP != "10.0.0.0/8" {
					t.Errorf("expected one CIDR rule for 10.0.0.0/8, got %+v", cidrRules)
				}
				if len(plRules) != 1 {
					t.Errorf("expected one prefix list rule, got %d", len(plRules))
				}
			},
		},
		{
			name: "prefix list only — no CIDRs — still gets default IPv4",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressPrefixLists: []string{"pl-0123456789abcdef0"},
				},
			},
			wantRuleCount: 2,
			checkRules: func(t *testing.T, rules []ec2model.IPPermission) {
				hasDefault := false
				for _, r := range rules {
					if len(r.IPRanges) > 0 && r.IPRanges[0].CIDRIP == "0.0.0.0/0" {
						hasDefault = true
					}
				}
				if !hasDefault {
					t.Error("expected default 0.0.0.0/0 rule when no ingressCidrs set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolvedIPv6 := tt.proxy.Spec.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv6Addressability
			if resolvedIPv6 == "" {
				resolvedIPv6 = "off"
			}
			rules, err := buildManagedSecurityGroupIngressPermissions(ir.New(infra), tt.proxy, resolvedIPv6)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rules) != tt.wantRuleCount {
				t.Fatalf("rule count = %d, want %d; rules = %+v", len(rules), tt.wantRuleCount, rules)
			}
			if tt.checkRules != nil {
				tt.checkRules(t, rules)
			}
		})
	}
}

// The UDP rules for HTTP/3 get the same sources as the TCP rules: every IPv4 and IPv6 CIDR and
// every prefix list.
func TestBuildManagedSecurityGroupIngressPermissions_HTTP3Sources(t *testing.T) {
	infra := &pb.Infra{
		Proxy: &pb.ProxyInfra{
			Name: "default/gw",
			Listeners: []*pb.ProxyListener{{
				Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.HTTPSProtocolType}},
				Http3: &pb.HTTP3Settings{},
			}},
		},
	}
	proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			IngressCidrs:       []string{"10.0.0.0/8", "2600:1f18::/32"},
			IngressPrefixLists: []string{"pl-0123456789abcdef0"},
		},
	}

	rules, err := buildManagedSecurityGroupIngressPermissions(ir.New(infra), proxy, "internet-facing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 6 {
		t.Fatalf("ingress rules count = %d, want 6", len(rules))
	}

	sources := func(protocol string) []string {
		var out []string
		for _, r := range rules {
			if r.IPProtocol != protocol || *r.FromPort != 443 || *r.ToPort != 443 {
				continue
			}
			for _, ipr := range r.IPRanges {
				out = append(out, ipr.CIDRIP)
			}
			for _, ipr := range r.IPv6Range {
				out = append(out, ipr.CIDRIPv6)
			}
			for _, pl := range r.PrefixLists {
				out = append(out, pl.ListID)
			}
		}
		slices.Sort(out)
		return out
	}
	want := []string{"10.0.0.0/8", "2600:1f18::/32", "pl-0123456789abcdef0"}
	for _, protocol := range []string{"tcp", "udp"} {
		if got := sources(protocol); !slices.Equal(got, want) {
			t.Errorf("%s sources on port 443 = %v, want %v", protocol, got, want)
		}
	}
}

func TestResolveIngressCIDRs(t *testing.T) {
	tests := []struct {
		name     string
		proxy    *elbproxyv1alpha1.ELBProxyConfiguration
		wantIPv4 []string
		wantIPv6 []string
	}{
		{
			name:     "no ingressCidrs and IPv6 disabled → default IPv4 only",
			proxy:    &elbproxyv1alpha1.ELBProxyConfiguration{},
			wantIPv4: []string{"0.0.0.0/0"},
			wantIPv6: nil,
		},
		{
			name: "no ingressCidrs and IPv6 enabled → default IPv4 + IPv6",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv6Addressability: "internal",
							},
						},
					},
				},
			},
			wantIPv4: []string{"0.0.0.0/0"},
			wantIPv6: []string{"::/0"},
		},
		{
			name: "IPv4-only ingressCidrs",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"10.0.0.0/8", "192.168.0.0/16"},
				},
			},
			wantIPv4: []string{"10.0.0.0/8", "192.168.0.0/16"},
			wantIPv6: nil,
		},
		{
			name: "IPv6-only ingressCidrs dropped when IPv6 disabled",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"2001:db8::/32", "fd00::/8"},
					// IPv6 not enabled
				},
			},
			wantIPv4: nil,
			wantIPv6: nil, // dropped because IPv6 disabled
		},
		{
			name: "IPv6-only ingressCidrs kept when IPv6 enabled",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"2001:db8::/32", "fd00::/8"},
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv6Addressability: "internal",
							},
						},
					},
				},
			},
			wantIPv4: nil,
			wantIPv6: []string{"2001:db8::/32", "fd00::/8"},
		},
		{
			name: "mixed IPv4 and IPv6 ingressCidrs — IPv6 dropped when disabled",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"10.0.0.0/8", "2001:db8::/32"},
					// IPv6 not enabled
				},
			},
			wantIPv4: []string{"10.0.0.0/8"},
			wantIPv6: nil,
		},
		{
			name: "mixed IPv4 and IPv6 ingressCidrs — both kept when IPv6 enabled",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"10.0.0.0/8", "2001:db8::/32"},
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv6Addressability: "internal",
							},
						},
					},
				},
			},
			wantIPv4: []string{"10.0.0.0/8"},
			wantIPv6: []string{"2001:db8::/32"},
		},
		{
			name: "ingressCidrs set overrides IPv6 default even when IPv6 enabled",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"10.0.0.0/8"},
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv6Addressability: "internal",
							},
						},
					},
				},
			},
			wantIPv4: []string{"10.0.0.0/8"},
			wantIPv6: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolvedIPv6 := tt.proxy.Spec.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv6Addressability
			if resolvedIPv6 == "" {
				resolvedIPv6 = "off"
			}
			gotIPv4, gotIPv6, err := resolveIngressCIDRs(tt.proxy, resolvedIPv6)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(gotIPv4) != len(tt.wantIPv4) {
				t.Fatalf("IPv4 CIDRs = %v, want %v", gotIPv4, tt.wantIPv4)
			}
			for i, want := range tt.wantIPv4 {
				if gotIPv4[i] != want {
					t.Errorf("IPv4[%d] = %q, want %q", i, gotIPv4[i], want)
				}
			}

			if len(gotIPv6) != len(tt.wantIPv6) {
				t.Fatalf("IPv6 CIDRs = %v, want %v", gotIPv6, tt.wantIPv6)
			}
			for i, want := range tt.wantIPv6 {
				if gotIPv6[i] != want {
					t.Errorf("IPv6[%d] = %q, want %q", i, gotIPv6[i], want)
				}
			}
		})
	}
}

func TestBuildManagedSecurityGroupIngressPermissions_IngressCidrs(t *testing.T) {
	infra := &pb.Infra{
		Proxy: &pb.ProxyInfra{
			Name: "default/gw",
			Listeners: []*pb.ProxyListener{
				{Ports: []*pb.ListenerPort{{ServicePort: 443, Protocol: ir.HTTPSProtocolType}}},
			},
		},
	}

	tests := []struct {
		name          string
		proxy         *elbproxyv1alpha1.ELBProxyConfiguration
		wantRuleCount int
		checkRule     func(t *testing.T, rules []ec2model.IPPermission)
	}{
		{
			name:          "no ingressCidrs → one IPv4 rule with 0.0.0.0/0",
			proxy:         &elbproxyv1alpha1.ELBProxyConfiguration{},
			wantRuleCount: 1,
			checkRule: func(t *testing.T, rules []ec2model.IPPermission) {
				if len(rules[0].IPRanges) != 1 || rules[0].IPRanges[0].CIDRIP != "0.0.0.0/0" {
					t.Errorf("expected default IPv4 CIDR 0.0.0.0/0, got %+v", rules[0].IPRanges)
				}
			},
		},
		{
			name: "custom IPv4 CIDR replaces default",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"10.0.0.0/8"},
				},
			},
			wantRuleCount: 1,
			checkRule: func(t *testing.T, rules []ec2model.IPPermission) {
				if len(rules[0].IPRanges) != 1 || rules[0].IPRanges[0].CIDRIP != "10.0.0.0/8" {
					t.Errorf("expected CIDR 10.0.0.0/8, got %+v", rules[0].IPRanges)
				}
			},
		},
		{
			name: "IPv6 CIDR produces IPv6 rule",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"2001:db8::/32"},
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv6Addressability: "internal",
							},
						},
					},
				},
			},
			wantRuleCount: 1,
			checkRule: func(t *testing.T, rules []ec2model.IPPermission) {
				if len(rules[0].IPv6Range) != 1 || rules[0].IPv6Range[0].CIDRIPv6 != "2001:db8::/32" {
					t.Errorf("expected IPv6 CIDR 2001:db8::/32, got %+v", rules[0].IPv6Range)
				}
			},
		},
		{
			name: "mixed CIDRs produce two rules for same port",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					IngressCidrs: []string{"10.0.0.0/8", "2001:db8::/32"},
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv6Addressability: "internal",
							},
						},
					},
				},
			},
			wantRuleCount: 2,
			checkRule: func(t *testing.T, rules []ec2model.IPPermission) {
				if *rules[0].FromPort != 443 || *rules[1].FromPort != 443 {
					t.Errorf("expected both rules on port 443")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolvedIPv6 := tt.proxy.Spec.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv6Addressability
			if resolvedIPv6 == "" {
				resolvedIPv6 = "off"
			}
			rules, err := buildManagedSecurityGroupIngressPermissions(ir.New(infra), tt.proxy, resolvedIPv6)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rules) != tt.wantRuleCount {
				t.Fatalf("rule count = %d, want %d; rules = %+v", len(rules), tt.wantRuleCount, rules)
			}
			if tt.checkRule != nil {
				tt.checkRule(t, rules)
			}
		})
	}
}

// A gateway whose routes have no backend ports yet still needs the xDS port open on the node
// security groups, or its proxy never gets its configuration. The Wasm module server port opens
// with it, since both are control-plane ports.
func TestReconcileNodeSGRules_OpensXDSWithoutBackendPorts(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockResolver := networking.NewMockNodeSGResolver(ctrl)
	mockResolver.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-node"}, nil)
	mockReconciler := networking.NewMockSecurityGroupReconciler(ctrl)
	var opened []int32
	mockReconciler.EXPECT().ReconcileIngress(gomock.Any(), "sg-node", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, perms []networking.IPPermissionInfo, _ ...networking.SecurityGroupReconcileOption) error {
			for _, perm := range perms {
				opened = append(opened, *perm.Permission.FromPort)
			}
			return nil
		})
	b := &securityGroupBuilder{
		nodeSGResolver:      mockResolver,
		sgReconciler:        mockReconciler,
		controllerNamespace: "envoy-gateway-system",
		logger:              testBuilderLogger(),
	}

	if err := b.reconcileNodeSGRules(context.Background(), "sg-backend", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assert.Equal(t, []int32{networking.DefaultXDSServerPort, networking.DefaultWasmServerPort}, opened,
		"want only the control-plane ports")
}

func TestReconcileNodeSGRules_NamespaceScopedLabels(t *testing.T) {
	// Verifies that reconcileNodeSGRules passes namespace-scoped labels to
	// ReconcileIngress, ensuring multi-tenant controllers don't revoke each
	// other's rules.
	const backendSGID = "sg-backend"
	backendPorts := []ir.PortProtocol{{Port: 8080, Protocol: "TCP"}}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockResolver := networking.NewMockNodeSGResolver(ctrl)
	mockResolver.EXPECT().ResolveNodeSGIDs(gomock.Any()).Return([]string{"sg-node"}, nil)

	mockReconciler := networking.NewMockSecurityGroupReconciler(ctrl)
	// Capture the permissions and options passed to ReconcileIngress.
	mockReconciler.EXPECT().ReconcileIngress(gomock.Any(), "sg-node", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, perms []networking.IPPermissionInfo, _ ...networking.SecurityGroupReconcileOption) error {
			// Verify permissions carry the controller-namespace label.
			if len(perms) == 0 {
				t.Fatal("expected at least one permission")
			}
			for _, p := range perms {
				if p.Labels == nil {
					t.Fatal("permission labels should not be nil")
				}
				if p.Labels["gateway.elbproxy.aws/controller-ns"] != "team-alpha" {
					t.Errorf("expected controller-ns label 'team-alpha', got %q", p.Labels["gateway.elbproxy.aws/controller-ns"])
				}
				if p.Labels["gateway.elbproxy.aws/managed-by"] != "elb-envoy-remote-infra-provider" {
					t.Errorf("expected managed-by label 'elb-envoy-remote-infra-provider', got %q", p.Labels["gateway.elbproxy.aws/managed-by"])
				}
			}
			return nil
		})

	b := &securityGroupBuilder{
		nodeSGResolver:      mockResolver,
		sgReconciler:        mockReconciler,
		controllerNamespace: "team-alpha",
		logger:              testBuilderLogger(),
	}

	err := b.reconcileNodeSGRules(context.Background(), backendSGID, backendPorts)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
