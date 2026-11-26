// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"context"
	"io"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
)

func TestBuildProxyName(t *testing.T) {
	tests := []struct {
		name           string
		clusterName    string // defaults to "test-cluster" when empty
		elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration
		proxyInfra     *pb.ProxyInfra
		want           string
	}{
		{
			name: "explicit name — returned as-is",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec:       elbproxyv1alpha1.ELBProxyConfigurationSpec{Name: "my-explicit-name"},
			},
			proxyInfra: &pb.ProxyInfra{Name: "default/elbproxy-gateway"},
			want:       "my-explicit-name",
		},
		{
			name: "non-merged: auto-generated from namespace/name key",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
			},
			proxyInfra: &pb.ProxyInfra{Name: "default/elbproxy-gateway"},
			want:       "elbp-defaultelbproxyg-8cd419533a",
		},
		{
			// Same gateway key as above but a different cluster name must yield a
			// different proxy name — proxy names are account-global, so identically
			// named gateways in two clusters must not collide.
			name:        "non-merged: different cluster name yields different proxy name for same key",
			clusterName: "other-cluster",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
			},
			proxyInfra: &pb.ProxyInfra{Name: "default/elbproxy-gateway"},
			want:       "elbp-defaultelbproxyg-ecc654ee4e",
		},
		{
			// Merged gateway key is a bare GatewayClass name — no slash
			name: "merged: auto-generated from bare GatewayClass name",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "envoy-gateway-system"},
			},
			proxyInfra: &pb.ProxyInfra{Name: "merged-eg"},
			want:       "elbp-mergedeg-757a5aff68",
		},
		{
			// Two different merged GatewayClass names must produce different proxy names.
			name: "merged: different class names produce different proxy names",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "envoy-gateway-system"},
			},
			proxyInfra: &pb.ProxyInfra{Name: "other-class"},
			want:       "elbp-otherclass-1989471e0a",
		},
		{
			// Create-only fields are in the hash, so a change gives the replacement a new name.
			name: "non-merged: roleArn changes the generated name",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec:       elbproxyv1alpha1.ELBProxyConfigurationSpec{RoleArn: "arn:aws:iam::123456789012:role/b"},
			},
			proxyInfra: &pb.ProxyInfra{Name: "default/elbproxy-gateway"},
			want:       "elbp-defaultelbproxyg-5382bb1e95",
		},
		{
			name: "non-merged: ipConfiguration changes the generated name",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
					IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
						Ipv6Addressability: "internal",
					}},
				}},
			},
			proxyInfra: &pb.ProxyInfra{Name: "default/elbproxy-gateway"},
			want:       "elbp-defaultelbproxyg-e7489fe21c",
		},
		{
			// The default written explicitly resolves to the same values, so the name is unchanged.
			name: "non-merged: explicit default ipConfiguration keeps the generated name",
			elbProxyConfig: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
					IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
						Ipv4Addressability: "internal", Ipv6Addressability: "off",
					}},
				}},
			},
			proxyInfra: &pb.ProxyInfra{Name: "default/elbproxy-gateway"},
			want:       "elbp-defaultelbproxyg-8cd419533a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusterName := tt.clusterName
			if clusterName == "" {
				clusterName = "test-cluster"
			}
			b := &ELBProxyBuilder{clusterName: clusterName}
			got, err := b.buildProxyName(ir.New(&pb.Infra{Proxy: tt.proxyInfra}), tt.elbProxyConfig)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateAccessLog(t *testing.T) {
	settingWithSinks := func(types ...egv1a1.ProxyAccessLogSinkType) egv1a1.ProxyAccessLogSetting {
		sinks := make([]egv1a1.ProxyAccessLogSink, 0, len(types))
		for _, ty := range types {
			sinks = append(sinks, egv1a1.ProxyAccessLogSink{Type: ty})
		}
		return egv1a1.ProxyAccessLogSetting{Sinks: sinks}
	}

	tests := []struct {
		name      string
		telemetry *egv1a1.ProxyTelemetry
		wantErr   bool
	}{
		{
			name:      "disable true passes",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{Disable: ptr.To(true)}},
			wantErr:   false,
		},
		{
			name: "OpenTelemetry sink passes",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{
				Settings: []egv1a1.ProxyAccessLogSetting{settingWithSinks(egv1a1.ProxyAccessLogSinkTypeOpenTelemetry)},
			}},
			wantErr: false,
		},
		{
			name: "ALS sink passes",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{
				Settings: []egv1a1.ProxyAccessLogSetting{settingWithSinks(egv1a1.ProxyAccessLogSinkTypeALS)},
			}},
			wantErr: false,
		},
		{
			name: "File sink is rejected",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{
				Settings: []egv1a1.ProxyAccessLogSetting{settingWithSinks(egv1a1.ProxyAccessLogSinkTypeFile)},
			}},
			wantErr: true,
		},
		{
			name: "mixed sinks including File is rejected",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{
				Settings: []egv1a1.ProxyAccessLogSetting{settingWithSinks(egv1a1.ProxyAccessLogSinkTypeOpenTelemetry, egv1a1.ProxyAccessLogSinkTypeFile)},
			}},
			wantErr: true,
		},
		{
			name:      "disable false with no settings is rejected (defaults to stdout)",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{Disable: ptr.To(false)}},
			wantErr:   true,
		},
		{
			name:      "empty settings is rejected (defaults to stdout)",
			telemetry: &egv1a1.ProxyTelemetry{AccessLog: &egv1a1.ProxyAccessLog{}},
			wantErr:   true,
		},
		{
			name:      "nil AccessLog is rejected (defaults to stdout)",
			telemetry: &egv1a1.ProxyTelemetry{},
			wantErr:   true,
		},
		{
			name:      "nil Telemetry is rejected (defaults to stdout)",
			telemetry: nil,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &egv1a1.EnvoyProxy{Spec: egv1a1.EnvoyProxySpec{Telemetry: tt.telemetry}}
			err := validateAccessLog(cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBuildProxyIPConfiguration(t *testing.T) {
	makeInfra := func(ipFamily *egv1a1.IPFamily) *pb.Infra {
		proxy := ir.NewProxyInfra()
		if ipFamily != nil {
			require.NoError(t, ir.SetProxyConfig(proxy, &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{IPFamily: ipFamily},
			}))
		}
		return &pb.Infra{Proxy: proxy}
	}
	ipv4 := egv1a1.IPv4
	ipv6 := egv1a1.IPv6
	dual := egv1a1.DualStack

	tests := []struct {
		name               string
		irInfra            *pb.Infra
		ipv4Addressability string
		ipv6Addressability string
		want               elbproxyaws.IpConfiguration
	}{
		{
			name:               "explicit internal/off — user-provided values respected",
			irInfra:            makeInfra(nil),
			ipv4Addressability: "internal",
			ipv6Addressability: "off",
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internal", Ipv6Addressability: "off",
			}},
		},
		{
			name:               "explicit internet-facing/internet-facing",
			irInfra:            makeInfra(nil),
			ipv4Addressability: "internet-facing",
			ipv6Addressability: "internet-facing",
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internet-facing", Ipv6Addressability: "internet-facing",
			}},
		},
		{
			name:    "nil irInfra — defaults to IPv4 internal/off",
			irInfra: nil,
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internal", Ipv6Addressability: "off",
			}},
		},
		{
			name:    "IPFamily unset (no EnvoyProxy config) — defaults to internal/off",
			irInfra: makeInfra(nil),
			// ipv4Addressability and ipv6Addressability intentionally left empty — user set nothing
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internal", Ipv6Addressability: "off",
			}},
		},
		{
			name:    "IPFamily IPv4 — defaults to internal/off",
			irInfra: makeInfra(&ipv4),
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internal", Ipv6Addressability: "off",
			}},
		},
		{
			name:    "IPFamily IPv6 — defaults to off/internal",
			irInfra: makeInfra(&ipv6),
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "off", Ipv6Addressability: "internal",
			}},
		},
		{
			name:    "IPFamily DualStack — defaults to internal/internal",
			irInfra: makeInfra(&dual),
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internal", Ipv6Addressability: "internal",
			}},
		},
		{
			name:               "IPFamily IPv6 but user overrides ipv4 to internet-facing",
			irInfra:            makeInfra(&ipv6),
			ipv4Addressability: "internet-facing",
			want: elbproxyaws.IpConfiguration{IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
				Ipv4Addressability: "internet-facing", Ipv6Addressability: "internal",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &ELBProxyBuilder{}
			proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv4Addressability: tt.ipv4Addressability,
								Ipv6Addressability: tt.ipv6Addressability,
							},
						},
					},
				},
			}
			assert.Equal(t, tt.want, b.buildProxyIPConfiguration(ir.New(tt.irInfra), proxy))
		})
	}
}

func TestBuildProxyNetworkConfiguration_SubnetDiscoveryAddressability(t *testing.T) {
	tests := []struct {
		name             string
		ipv4, ipv6       string
		want             networking.SubnetAddressability
		wantIPv6Required bool
	}{
		{
			name:             "IPv4 internal drives subnet discovery",
			ipv4:             elbproxyaws.IpAddressabilityInternal,
			ipv6:             elbproxyaws.IpAddressabilityOff,
			want:             networking.SubnetAddressabilityInternal,
			wantIPv6Required: false,
		},
		{
			name:             "dual-stack internet-facing uses IPv4 addressability and requires IPv6 subnets",
			ipv4:             elbproxyaws.IpAddressabilityInternetFacing,
			ipv6:             elbproxyaws.IpAddressabilityInternetFacing,
			want:             networking.SubnetAddressabilityInternetFacing,
			wantIPv6Required: true,
		},
		{
			name:             "IPv6-only internal falls back to IPv6 addressability",
			ipv4:             elbproxyaws.IpAddressabilityOff,
			ipv6:             elbproxyaws.IpAddressabilityInternal,
			want:             networking.SubnetAddressabilityInternal,
			wantIPv6Required: true,
		},
		{
			name:             "IPv6-only internet-facing falls back to IPv6 addressability",
			ipv4:             elbproxyaws.IpAddressabilityOff,
			ipv6:             elbproxyaws.IpAddressabilityInternetFacing,
			want:             networking.SubnetAddressabilityInternetFacing,
			wantIPv6Required: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			resolver := networking.NewMockSubnetsResolver(ctrl)
			resolver.EXPECT().
				ResolveViaNameOrIDSlice(gomock.Any(), gomock.Eq([]string{"subnet-a", "subnet-b"}), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ []string, opts ...networking.SubnetsResolveOption) ([]ec2types.Subnet, error) {
					resolveOpts := networking.SubnetsResolveOptions{}
					resolveOpts.ApplyOptions(opts)
					assert.Equal(t, tt.want, resolveOpts.Addressability)
					assert.Equal(t, tt.wantIPv6Required, resolveOpts.IPv6Required)
					return []ec2types.Subnet{
						{SubnetId: awssdk.String("subnet-a")},
						{SubnetId: awssdk.String("subnet-b")},
					}, nil
				})

			builder := &ELBProxyBuilder{
				logger: logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo),
				subnetsBuilder: newELBProxySubnetsBuilder(
					resolver,
					tracking.NewProvider("test-cluster"),
					elbproxy.NewMockTaggingManager(ctrl),
				),
			}
			proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						VpcConfiguration: elbproxyv1alpha1.ProxyVpcUserConfiguration{
							Subnets: []elbproxyv1alpha1.SubnetDetails{{SubnetId: "subnet-a"}, {SubnetId: "subnet-b"}},
						},
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv4Addressability: tt.ipv4,
								Ipv6Addressability: tt.ipv6,
							},
						},
					},
				},
			}
			stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "default", Name: "gateway"}, false)

			out, err := builder.buildProxyNetworkConfiguration(context.Background(), stack, nil, proxy, nil, nil)

			require.NoError(t, err)
			assert.Equal(t, []elbproxyaws.SubnetDetails{{SubnetId: "subnet-a"}, {SubnetId: "subnet-b"}}, out.networkConfig.VpcConfiguration.Subnets)
		})
	}
}

func TestBuildProxyNetworkConfiguration_Zones(t *testing.T) {
	tests := []struct {
		name      string
		resolved  []ec2types.Subnet
		wantZones []string
	}{
		{
			name: "sorted and deduplicated",
			resolved: []ec2types.Subnet{
				{SubnetId: awssdk.String("subnet-c"), AvailabilityZone: awssdk.String("us-east-1c")},
				{SubnetId: awssdk.String("subnet-a"), AvailabilityZone: awssdk.String("us-east-1a")},
				{SubnetId: awssdk.String("subnet-b"), AvailabilityZone: awssdk.String("us-east-1b")},
				{SubnetId: awssdk.String("subnet-b2"), AvailabilityZone: awssdk.String("us-east-1b")},
			},
			wantZones: []string{"us-east-1a", "us-east-1b", "us-east-1c"},
		},
		{
			name: "single zone",
			resolved: []ec2types.Subnet{
				{SubnetId: awssdk.String("subnet-a"), AvailabilityZone: awssdk.String("us-east-1a")},
			},
			wantZones: []string{"us-east-1a"},
		},
		{
			name: "blank availability zone is skipped",
			resolved: []ec2types.Subnet{
				{SubnetId: awssdk.String("subnet-a"), AvailabilityZone: awssdk.String("us-east-1a")},
				{SubnetId: awssdk.String("subnet-x"), AvailabilityZone: awssdk.String("")},
			},
			wantZones: []string{"us-east-1a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			resolver := networking.NewMockSubnetsResolver(ctrl)
			resolver.EXPECT().
				ResolveViaNameOrIDSlice(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(tt.resolved, nil)

			builder := &ELBProxyBuilder{
				logger: logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo),
				subnetsBuilder: newELBProxySubnetsBuilder(
					resolver,
					tracking.NewProvider("test-cluster"),
					elbproxy.NewMockTaggingManager(ctrl),
				),
			}
			proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
						VpcConfiguration: elbproxyv1alpha1.ProxyVpcUserConfiguration{
							Subnets: []elbproxyv1alpha1.SubnetDetails{{SubnetId: "subnet-a"}},
						},
						IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
							IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
								Ipv4Addressability: elbproxyaws.IpAddressabilityInternal,
								Ipv6Addressability: elbproxyaws.IpAddressabilityOff,
							},
						},
					},
				},
			}
			stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "default", Name: "gateway"}, false)

			out, err := builder.buildProxyNetworkConfiguration(context.Background(), stack, nil, proxy, nil, nil)

			require.NoError(t, err)
			assert.Equal(t, tt.wantZones, out.zones,
				"zones must be the sorted, deduplicated AvailabilityZone names of the resolved subnets")
		})
	}
}

func TestBuildProxyAttributes(t *testing.T) {
	tests := []struct {
		name        string
		attributes  []elbproxyv1alpha1.Attribute
		proxyConfig *egv1a1.EnvoyProxy
		want        []elbproxyaws.Attribute
		wantErr     string
	}{
		{
			name:       "nil attributes",
			attributes: nil,
			want:       nil,
		},
		{
			name:       "empty attributes",
			attributes: []elbproxyv1alpha1.Attribute{},
			want:       nil,
		},
		{
			name: "single attribute",
			attributes: []elbproxyv1alpha1.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
			},
		},
		{
			name: "multiple attributes",
			attributes: []elbproxyv1alpha1.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false"},
				{Key: elbproxyaws.AttributeKeyWafFailOpenEnabled, Value: "true"},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false"},
				{Key: elbproxyaws.AttributeKeyWafFailOpenEnabled, Value: "true"},
			},
		},
		{
			name: "default log level only",
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentDefault: egv1a1.LogLevelDebug,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug"},
			},
		},
		{
			name: "component log levels sorted",
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentDefault:    egv1a1.LogLevelInfo,
							egv1a1.LogComponentUpstream:   egv1a1.LogLevelDebug,
							egv1a1.LogComponentConnection: egv1a1.LogLevelTrace,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "info"},
				{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: "connection:trace,upstream:debug"},
			},
		},
		{
			name: "component log level without default",
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentHTTP: egv1a1.LogLevelWarn,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: "http:warn"},
			},
		},
		{
			name: "reserved log level key in spec attributes is rejected",
			attributes: []elbproxyv1alpha1.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "error"},
			},
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentDefault: egv1a1.LogLevelDebug,
						},
					},
				},
			},
			wantErr: "envoy.log_level\" is managed by the controller and cannot be set via ELBProxyConfiguration.spec.attributes",
		},
		{
			name: "reserved component log level key in spec attributes is rejected",
			attributes: []elbproxyv1alpha1.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: "http:debug"},
			},
			wantErr: "envoy.component_log_level\" is managed by the controller and cannot be set via ELBProxyConfiguration.spec.attributes",
		},
		{
			name: "spec attributes combined with derived log level",
			attributes: []elbproxyv1alpha1.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
			},
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentDefault: egv1a1.LogLevelDebug,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{Attributes: tt.attributes},
			}
			got, err := buildProxyAttributes(proxy, tt.proxyConfig)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildComponentLogLevel(t *testing.T) {
	tests := []struct {
		name   string
		levels map[egv1a1.ProxyLogComponent]egv1a1.LogLevel
		want   string
	}{
		{
			name:   "nil map yields empty string",
			levels: nil,
			want:   "",
		},
		{
			name: "only default component yields empty string",
			levels: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
				egv1a1.LogComponentDefault: egv1a1.LogLevelInfo,
			},
			want: "",
		},
		{
			name: "single component",
			levels: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
				egv1a1.LogComponentHTTP: egv1a1.LogLevelDebug,
			},
			want: "http:debug",
		},
		{
			name: "default component is excluded from component string",
			levels: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
				egv1a1.LogComponentDefault:  egv1a1.LogLevelInfo,
				egv1a1.LogComponentUpstream: egv1a1.LogLevelDebug,
			},
			want: "upstream:debug",
		},
		{
			name: "multiple components are sorted deterministically",
			levels: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
				egv1a1.LogComponentUpstream:   egv1a1.LogLevelDebug,
				egv1a1.LogComponentConnection: egv1a1.LogLevelTrace,
				egv1a1.LogComponentRouter:     egv1a1.LogLevelInfo,
				egv1a1.LogComponentAdmin:      egv1a1.LogLevelError,
			},
			want: "admin:error,connection:trace,router:info,upstream:debug",
		},
		{
			name: "warn components passed through verbatim",
			levels: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
				egv1a1.LogComponentConnection: egv1a1.LogLevelWarn,
				egv1a1.LogComponentUpstream:   egv1a1.LogLevelWarn,
			},
			want: "connection:warn,upstream:warn",
		},
		{
			name: "component with empty level is skipped",
			levels: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
				egv1a1.LogComponentHTTP:     egv1a1.LogLevelDebug,
				egv1a1.LogComponentUpstream: egv1a1.LogLevel(""),
			},
			want: "http:debug",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildComponentLogLevel(tt.levels))
		})
	}
}

func TestBuildLogLevelAttributes(t *testing.T) {
	tests := []struct {
		name        string
		proxyConfig *egv1a1.EnvoyProxy
		want        []elbproxyaws.Attribute
	}{
		{
			name:        "nil proxy config yields no attributes",
			proxyConfig: nil,
			want:        nil,
		},
		{
			name:        "empty logging yields no attributes",
			proxyConfig: &egv1a1.EnvoyProxy{},
			want:        nil,
		},
		{
			name: "default and components produce both attributes",
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentDefault:    egv1a1.LogLevelInfo,
							egv1a1.LogComponentUpstream:   egv1a1.LogLevelDebug,
							egv1a1.LogComponentConnection: egv1a1.LogLevelTrace,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "info"},
				{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: "connection:trace,upstream:debug"},
			},
		},
		{
			name: "only default produces only log_level attribute",
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentDefault: egv1a1.LogLevelWarn,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "warn"},
			},
		},
		{
			name: "only components produce only component_log_level attribute",
			proxyConfig: &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Logging: egv1a1.ProxyLogging{
						Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
							egv1a1.LogComponentFilter: egv1a1.LogLevelDebug,
						},
					},
				},
			},
			want: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: "filter:debug"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildLogLevelAttributes(tt.proxyConfig))
		})
	}
}
