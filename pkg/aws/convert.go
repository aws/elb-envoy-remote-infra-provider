// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elbproxy"
	elbproxytypes "github.com/aws/aws-sdk-go-v2/service/elbproxy/types"
)

func toProxyDetails_create(r elbproxy.CreateProxyOutput) *ProxyDetails {
	d := &ProxyDetails{
		Arn:          *r.Arn,
		DnsName:      *r.DnsName,
		Name:         *r.Name,
		ProxyVersion: *r.ProxyVersion,
		RoleArn:      awssdk.ToString(r.RoleArn),
		Type:         string(r.Type),
	}
	if s := r.Status; s != nil {
		d.Status = toProxyStatus(s)
	}
	if nc := r.NetworkConfiguration; nc != nil {
		if ic := nc.IpConfiguration; ic != nil {
			if iac := ic.IpAddressingConfiguration; iac != nil {
				d.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration = IpAddressingConfiguration{
					Ipv4Addressability: string(iac.Ipv4Addressability),
					Ipv6Addressability: string(iac.Ipv6Addressability),
				}
			}
		}
		if vc := nc.VpcConfiguration; vc != nil {
			for _, az := range vc.AvailabilityZones {
				d.NetworkConfiguration.VpcConfiguration.AvailabilityZones = append(
					d.NetworkConfiguration.VpcConfiguration.AvailabilityZones,
					SubnetAndZone{SubnetId: *az.SubnetId, ZoneId: *az.ZoneId},
				)
			}
			for _, sg := range vc.SecurityGroups {
				d.NetworkConfiguration.VpcConfiguration.SecurityGroups = append(
					d.NetworkConfiguration.VpcConfiguration.SecurityGroups,
					SecurityGroupDetails{SecurityGroupId: *sg.SecurityGroupId},
				)
			}
		}
	}
	return d
}

func toProxyDetails_get(r *elbproxy.GetProxyOutput) *ProxyDetails {
	d := &ProxyDetails{
		Arn:          *r.Arn,
		DnsName:      *r.DnsName,
		Name:         *r.Name,
		ProxyVersion: *r.ProxyVersion,
		RoleArn:      awssdk.ToString(r.RoleArn),
		Type:         string(r.Type),
	}
	if s := r.Status; s != nil {
		d.Status = toProxyStatus(s)
	}
	if nc := r.NetworkConfiguration; nc != nil {
		if ic := nc.IpConfiguration; ic != nil {
			if iac := ic.IpAddressingConfiguration; iac != nil {
				d.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration = IpAddressingConfiguration{
					Ipv4Addressability: string(iac.Ipv4Addressability),
					Ipv6Addressability: string(iac.Ipv6Addressability),
				}
			}
		}
		if vc := nc.VpcConfiguration; vc != nil {
			for _, az := range vc.AvailabilityZones {
				d.NetworkConfiguration.VpcConfiguration.AvailabilityZones = append(
					d.NetworkConfiguration.VpcConfiguration.AvailabilityZones,
					SubnetAndZone{SubnetId: *az.SubnetId, ZoneId: *az.ZoneId},
				)
			}
			for _, sg := range vc.SecurityGroups {
				d.NetworkConfiguration.VpcConfiguration.SecurityGroups = append(
					d.NetworkConfiguration.VpcConfiguration.SecurityGroups,
					SecurityGroupDetails{SecurityGroupId: *sg.SecurityGroupId},
				)
			}
		}
	}
	if cm := r.ConfigurationMetadata; cm != nil {
		d.ConfigurationMetadata = toConfigurationMetadata(cm)
	}
	d.CustomerKmsKeyArn = r.CustomerKmsKeyArn
	return d
}

func toProxyDetails_updateNetwork(r *elbproxy.UpdateProxyNetworkConfigurationOutput) *ProxyDetails {
	d := &ProxyDetails{
		Arn:          *r.Arn,
		DnsName:      *r.DnsName,
		Name:         *r.Name,
		ProxyVersion: *r.ProxyVersion,
		RoleArn:      awssdk.ToString(r.RoleArn),
		Type:         string(r.Type),
	}
	if s := r.Status; s != nil {
		d.Status = toProxyStatus(s)
	}
	if nc := r.NetworkConfiguration; nc != nil {
		if ic := nc.IpConfiguration; ic != nil {
			if iac := ic.IpAddressingConfiguration; iac != nil {
				d.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration = IpAddressingConfiguration{
					Ipv4Addressability: string(iac.Ipv4Addressability),
					Ipv6Addressability: string(iac.Ipv6Addressability),
				}
			}
		}
		if vc := nc.VpcConfiguration; vc != nil {
			for _, az := range vc.AvailabilityZones {
				d.NetworkConfiguration.VpcConfiguration.AvailabilityZones = append(
					d.NetworkConfiguration.VpcConfiguration.AvailabilityZones,
					SubnetAndZone{SubnetId: *az.SubnetId, ZoneId: *az.ZoneId},
				)
			}
			for _, sg := range vc.SecurityGroups {
				d.NetworkConfiguration.VpcConfiguration.SecurityGroups = append(
					d.NetworkConfiguration.VpcConfiguration.SecurityGroups,
					SecurityGroupDetails{SecurityGroupId: *sg.SecurityGroupId},
				)
			}
		}
	}
	return d
}

func toAPISubnetDetails(subnets []SubnetDetails) []elbproxytypes.SubnetDetails {
	out := make([]elbproxytypes.SubnetDetails, len(subnets))
	for i, s := range subnets {
		out[i] = elbproxytypes.SubnetDetails{SubnetId: new(s.SubnetId)}
	}
	return out
}

// toAPISecurityGroupDetails returns nil for an empty list so the field is omitted,
// which tells the API to keep the proxy's current security groups.
func toAPISecurityGroupDetails(sgs []SecurityGroupDetails) []elbproxytypes.SecurityGroupDetails {
	if len(sgs) == 0 {
		return nil
	}
	out := make([]elbproxytypes.SecurityGroupDetails, len(sgs))
	for i, sg := range sgs {
		out[i] = elbproxytypes.SecurityGroupDetails{SecurityGroupId: new(sg.SecurityGroupId)}
	}
	return out
}

func toProxyStatus(s *elbproxytypes.ProxyStatus) ProxyStatus {
	ps := ProxyStatus{Code: string(s.Code)}
	for _, r := range s.Reasons {
		ps.Reasons = append(ps.Reasons, ProxyStatusReason{
			Code:    awssdk.ToString(r.Code),
			Message: awssdk.ToString(r.Message),
			Target:  awssdk.ToString(r.Target),
		})
	}
	return ps
}

func toConfigurationMetadata(cm *elbproxytypes.ConfigurationMetadata) *ConfigurationMetadata {
	if cm == nil {
		return nil
	}
	out := &ConfigurationMetadata{
		ConfigArchiveHash: cm.ConfigArchiveHash,
		ConfigFileHash:    cm.ConfigFileHash,
	}
	if cm.ConfigArchiveLastUpdatedAt != nil {
		t := cm.ConfigArchiveLastUpdatedAt.String()
		out.ConfigArchiveLastUpdatedAt = &t
	}
	if cm.ConfigFileLastUpdatedAt != nil {
		t := cm.ConfigFileLastUpdatedAt.String()
		out.ConfigFileLastUpdatedAt = &t
	}
	return out
}

func toAttributeDetailsList(attrs []elbproxytypes.AttributeDetails) []AttributeDetails {
	out := make([]AttributeDetails, len(attrs))
	for i, a := range attrs {
		out[i] = AttributeDetails{
			Key:          *a.Key,
			Value:        *a.Value,
			DefaultValue: a.DefaultValue,
		}
	}
	return out
}
