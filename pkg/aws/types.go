// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

// Attribute is a key-value pair for proxy configuration.
type Attribute struct {
	Key   string
	Value string
}

const (
	// AttributeKeyDeletionProtectionEnabled controls whether the Proxy can be deleted.
	// When "true", DeleteProxy will fail. Default is "false".
	AttributeKeyDeletionProtectionEnabled = "deletion_protection.enabled"

	// AttributeKeyWafWebAclArn is the customer's WAF web ACL that protects the Proxy.
	// Set it at creation to enable WAF; empty or absent means WAF is disabled
	// (the default). It cannot be changed with UpdateProxyAttributes.
	AttributeKeyWafWebAclArn = "waf.web_acl_arn"

	// AttributeKeyWafFailOpenEnabled controls how the Proxy handles requests when
	// WAF is unavailable. When "true", the Proxy will fail-open. Default is "false".
	AttributeKeyWafFailOpenEnabled = "waf.fail_open.enabled"

	// AttributeKeyEnvoyLogLevel sets the default Envoy application log level.
	// Controller-managed: this attribute is derived by the controller from
	// EnvoyProxy spec.logging.level (the single source of truth for log level)
	// and is NOT user-settable via ELBProxyConfiguration spec.attributes
	AttributeKeyEnvoyLogLevel = "envoy.log_level"

	// AttributeKeyEnvoyComponentLogLevel sets per-component Envoy log levels as a
	// comma-separated list of component:level pairs (e.g.
	// "upstream:debug,connection:trace").
	//
	// Controller-managed: like AttributeKeyEnvoyLogLevel, this attribute is
	// derived from EnvoyProxy spec.logging.level and is NOT user-settable via
	// ELBProxyConfiguration spec.attributes.
	AttributeKeyEnvoyComponentLogLevel = "envoy.component_log_level"
)

// AttributeDetails extends Attribute with an optional default value.
// DefaultValue is nil for read-only attributes that cannot be reset.
type AttributeDetails struct {
	Key          string
	Value        string
	DefaultValue *string
}

// ProxyConfiguration holds the bootstrap configuration for a proxy.
type ProxyConfiguration struct {
	BootstrapFile []byte
	ArchiveBytes  []byte
}

// IpAddressability values for IPv4 and IPv6 addressing configuration.
const (
	IpAddressabilityInternal       = "internal"
	IpAddressabilityInternetFacing = "internet-facing"
	IpAddressabilityOff            = "off"
)

// IpAddressingConfiguration holds IPv4/IPv6 addressability settings.
type IpAddressingConfiguration struct {
	Ipv4Addressability string
	Ipv6Addressability string
}

// IpConfiguration wraps IP addressing configuration.
type IpConfiguration struct {
	IpAddressingConfiguration IpAddressingConfiguration
}

// SubnetDetails identifies a subnet.
type SubnetDetails struct {
	SubnetId string
}

// SecurityGroupDetails identifies a security group.
type SecurityGroupDetails struct {
	SecurityGroupId string
}

// VpcUserConfiguration holds VPC settings for mutating API calls.
type VpcUserConfiguration struct {
	Subnets        []SubnetDetails
	SecurityGroups []SecurityGroupDetails
}

// NetworkUserConfiguration holds network settings for mutating API calls.
type NetworkUserConfiguration struct {
	IpConfiguration  IpConfiguration
	VpcConfiguration VpcUserConfiguration
}

// SubnetAndZone pairs a subnet with its availability zone.
type SubnetAndZone struct {
	SubnetId string
	ZoneId   string
}

// VpcConfiguration holds VPC settings returned from read API calls.
type VpcConfiguration struct {
	AvailabilityZones []SubnetAndZone
	SecurityGroups    []SecurityGroupDetails
}

// NetworkConfiguration holds network settings returned from read API calls.
type NetworkConfiguration struct {
	IpConfiguration  IpConfiguration
	VpcConfiguration VpcConfiguration
}

// ProxyStatusReason holds a single reason for a proxy's status.
type ProxyStatusReason struct {
	Code    string
	Message string
	Target  string
}

// ProxyStatus holds the status of a proxy.
type ProxyStatus struct {
	Code    string
	Reasons []ProxyStatusReason
}

// Proxy status codes returned by the ELBProxy API.
const (
	ProxyStatusActive                 = "active"
	ProxyStatusActionRequired         = "action-required"
	ProxyStatusCreateInProgress       = "create-in-progress"
	ProxyStatusCreateFailed           = "create-failed"
	ProxyStatusUpdateInProgress       = "update-in-progress"
	ProxyStatusCancelUpdateInProgress = "cancel-update-in-progress"
	ProxyStatusDeleteInProgress       = "delete-in-progress"
	ProxyStatusDeleted                = "deleted"
)

// DeepCopy returns a deep copy of ProxyDetails, satisfying watchable.DeepCopier.
func (p ProxyDetails) DeepCopy() ProxyDetails {
	copy := p
	if p.Status.Reasons != nil {
		copy.Status.Reasons = append([]ProxyStatusReason{}, p.Status.Reasons...)
	}
	if p.NetworkConfiguration.VpcConfiguration.AvailabilityZones != nil {
		copy.NetworkConfiguration.VpcConfiguration.AvailabilityZones = append([]SubnetAndZone{}, p.NetworkConfiguration.VpcConfiguration.AvailabilityZones...)
	}
	if p.NetworkConfiguration.VpcConfiguration.SecurityGroups != nil {
		copy.NetworkConfiguration.VpcConfiguration.SecurityGroups = append([]SecurityGroupDetails{}, p.NetworkConfiguration.VpcConfiguration.SecurityGroups...)
	}
	return copy
}

// ConfigurationMetadata holds metadata about a proxy's configuration.
type ConfigurationMetadata struct {
	ConfigArchiveHash          *string
	ConfigArchiveLastUpdatedAt *string
	ConfigFileHash             *string
	ConfigFileLastUpdatedAt    *string
}

// ProxyDetails holds the common response fields for proxy operations.
type ProxyDetails struct {
	Arn                   string
	DnsName               string
	Name                  string
	NetworkConfiguration  NetworkConfiguration
	ProxyVersion          string
	RoleArn               string
	Status                ProxyStatus
	Type                  string
	ConfigurationMetadata *ConfigurationMetadata
	CustomerKmsKeyArn     *string
}

// CreateProxyInput holds the parameters for creating a proxy.
type CreateProxyInput struct {
	Name                 string
	Type                 string
	RoleArn              string
	ProxyVersion         string
	Attributes           []Attribute
	Configuration        ProxyConfiguration
	NetworkConfiguration NetworkUserConfiguration
	Tags                 map[string]string
	CustomerKmsKeyArn    *string
}

// TagResourceInput holds the parameters for tagging a resource.
type TagResourceInput struct {
	ResourceArn string
	Tags        map[string]string
}

// UntagResourceInput holds the parameters for untagging a resource.
type UntagResourceInput struct {
	ResourceArn string
	TagKeys     []string
}

// ListTagsForResourceInput holds the parameters for listing tags on a resource.
type ListTagsForResourceInput struct {
	ResourceArn string
}

// ListTagsForResourceOutput holds the response from listing tags on a resource.
type ListTagsForResourceOutput struct {
	Tags map[string]string
}

// GetProxyAttributesInput holds the parameters for getting proxy attributes.
type GetProxyAttributesInput struct {
	ProxyIdentifier string
}

// GetProxyAttributesOutput holds the response from getting proxy attributes.
type GetProxyAttributesOutput struct {
	Attributes []AttributeDetails
}

// UpdateProxyAttributesInput holds the parameters for updating proxy attributes.
type UpdateProxyAttributesInput struct {
	ProxyIdentifier string
	Attributes      []Attribute
}

// UpdateProxyAttributesOutput holds the response from updating proxy attributes.
type UpdateProxyAttributesOutput struct {
	Attributes []AttributeDetails
}

// UpdateProxyNetworkConfigurationInput holds the parameters for updating proxy network configuration.
type UpdateProxyNetworkConfigurationInput struct {
	ProxyIdentifier  string
	VpcConfiguration VpcUserConfiguration
}

// UpdateProxyConfigurationInput holds the parameters for updating proxy configuration.
type UpdateProxyConfigurationInput struct {
	ProxyIdentifier   string
	ArchiveBytes      []byte
	BootstrapFile     []byte
	CustomerKmsKeyArn *string
}
