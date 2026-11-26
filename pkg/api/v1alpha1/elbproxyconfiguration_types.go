// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=elbpc
// +kubebuilder:subresource:status
// +kubebuilder:storageversion

// ELBProxyConfiguration is a CRD for ELB for Envoy proxy configuration.
//
// ELBProxyConfiguration is a direct Gateway API policy: it attaches to one or more Gateway
// or GatewayClass resources via spec.targetRefs. When a Gateway is targeted, the
// ELBProxyConfiguration must live in the same namespace as the Gateway.
//
// When a GatewayClass is targeted, the ELBProxyConfiguration must live in the controller's own namespace
// GatewayClass-scope targetRefs from ELBProxyConfigurations in any other namespace are ignored.
type ELBProxyConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ELBProxyConfigurationSpec   `json:"spec,omitempty"`
	Status ELBProxyConfigurationStatus `json:"status,omitempty"`
}

type ELBProxyConfigurationSpec struct {
	// TargetRefs are the Gateway or GatewayClass resources this ELBProxyConfiguration
	// attaches to.
	//
	// Only group "gateway.networking.k8s.io" with kind "Gateway" or
	// "GatewayClass" are accepted. Gateway targets resolve within the
	// ELBProxyConfiguration's own namespace; GatewayClass targets are honored only
	// when the ELBProxyConfiguration lives in the controller's own namespace
	// When multiple ELBProxyConfigurations resolve to the same Gateway, a Gateway-scoped
	// attachment overrides a GatewayClass-scoped one. Ties within the same
	// scope are broken by creationTimestamp, then namespace, then name.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(r, r.group == 'gateway.networking.k8s.io' && (r.kind == 'Gateway' || r.kind == 'GatewayClass'))",message="targetRefs must reference gateway.networking.k8s.io/Gateway or gateway.networking.k8s.io/GatewayClass"
	TargetRefs []gwapiv1a2.LocalPolicyTargetReference `json:"targetRefs"`

	// Name of the proxy. If unspecified, it is generated automatically.
	//
	// Changing it replaces the proxy: the controller deletes it and creates a new one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]+$|^[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9]$`
	// +optional
	Name string `json:"name,omitempty"`

	// The Amazon Resource Name (ARN) of the IAM role that provides permissions to
	// the code that your Envoy configuration runs on the proxy, such as filters and
	// extensions. These are the permissions that this code needs when it calls AWS
	// services.
	//
	// Changing it replaces the proxy: the controller deletes it and creates a new one.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^arn:[a-z-]+:iam::[0-9]{12}:role/.+$`
	RoleArn string `json:"roleArn"`

	// Attributes for proxy configuration. Each key can appear only once.
	// +optional
	// +listType=map
	// +listMapKey=key
	Attributes []Attribute `json:"attributes,omitempty"`

	// NetworkConfiguration for VPC and IP settings
	NetworkConfiguration ProxyNetworkUserConfiguration `json:"networkConfiguration"`

	// IngressCidrs restricts which CIDR ranges are allowed to reach the proxy's
	// frontend security group. Applies to all listener ports.
	//
	// Each entry must be a valid IPv4 CIDR or IPv6 CIDR
	//
	// If unset, defaults to ["0.0.0.0/0", "::/0"] — open to all traffic.
	// Only applies when the controller manages the frontend SG (i.e. when
	// spec.networkConfiguration.vpcConfiguration.securityGroups is not set).
	// +optional
	// +kubebuilder:validation:MaxItems=100
	IngressCidrs []string `json:"ingressCidrs,omitempty"`

	// IngressPrefixLists specifies AWS prefix list IDs whose members are allowed
	// to reach the proxy's frontend security group. Applies to all listener ports.
	//
	// Each entry must be a valid AWS prefix list ID (e.g. "pl-0123456789abcdef0").
	//
	// Only applies when the controller manages the frontend SG (i.e. when
	// spec.networkConfiguration.vpcConfiguration.securityGroups is not set).
	// +optional
	// +kubebuilder:validation:MaxItems=30
	IngressPrefixLists []string `json:"ingressPrefixLists,omitempty"`

	// Tags defines list of Tags on the Proxy.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// CustomerKmsKeyArn is the ARN of a customer-managed KMS key (CMK) used to
	// encrypt the proxy's configuration content. If unset, the default
	// service-owned key is used.
	// +optional
	CustomerKmsKeyArn *string `json:"customerKmsKeyArn,omitempty"`
}

type Attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ProxyNetworkUserConfiguration struct {
	// VpcConfiguration defines VPC-specific settings including subnets and security groups.
	// +optional
	VpcConfiguration ProxyVpcUserConfiguration `json:"vpcConfiguration,omitempty"`
	// IpConfiguration defines IP addressing settings for IPv4 and IPv6.
	// Changing it replaces the proxy: the controller deletes it and creates a new one.
	// +optional
	IpConfiguration ProxyIpConfiguration `json:"ipConfiguration,omitempty"`
}

type ProxyVpcUserConfiguration struct {
	// Subnets for the proxy. If unspecified, the controller will use subnet Auto-Discovery.
	// When set, provide at least 2 subnets, each in a different Availability Zone,
	// all in the same VPC.
	// +optional
	// +kubebuilder:validation:MinItems=2
	Subnets []SubnetDetails `json:"subnets,omitempty"`

	// SecurityGroups for the proxy. If unspecified, the controller will use default security groups.
	// +optional
	SecurityGroups []SecurityGroupDetails `json:"securityGroups,omitempty"`
}

type SubnetDetails struct {
	SubnetId string `json:"subnetId"`
}

type SecurityGroupDetails struct {
	SecurityGroupId string `json:"securityGroupId"`
}

type ProxyIpConfiguration struct {
	// IpAddressingConfiguration defines IPv4 and IPv6 addressability settings.
	// If unset, defaults are derived from the EnvoyProxy IPFamily:
	//   IPv4 (default): ipv4=internal, ipv6=off
	//   IPv6:           ipv4=off,      ipv6=internal
	//   DualStack:      ipv4=internal, ipv6=internal
	// "internal" (default) means VPC-only; use "internet-facing" to expose publicly.
	// The ELB Proxy API rejects these combinations: both off; IPv4 internet-facing
	// with IPv6 internal; and IPv4 internal with IPv6 internet-facing. Quote "off"
	// in YAML, since an unquoted off is parsed as the boolean false.
	// +optional
	IpAddressingConfiguration ProxyIpAddressingConfiguration `json:"ipAddressingConfiguration,omitempty"`
}

type ProxyIpAddressingConfiguration struct {
	// Ipv4Addressability defines IPv4 addressing mode.
	// If unset, defaults are derived from the EnvoyProxy IPFamily. "internal" is the default.
	// +kubebuilder:validation:Enum=off;internal;internet-facing
	// +optional
	Ipv4Addressability string `json:"ipv4Addressability,omitempty"`
	// Ipv6Addressability defines IPv6 addressing mode.
	// If unset, defaults are derived from the EnvoyProxy IPFamily. "off" is the default for IPv4-only.
	// +kubebuilder:validation:Enum=off;internal;internet-facing
	// +optional
	Ipv6Addressability string `json:"ipv6Addressability,omitempty"`
}

// ELBProxyConfigurationStatus defines the observed state of ELBProxyConfiguration
type ELBProxyConfigurationStatus struct {
	// Conditions describe the ELBProxyConfiguration.
	//
	// Known condition types:
	//   - "Accepted": True when this is the winning policy for at least one target
	//     Gateway; False/Conflicted or False/InvalidTargetRef otherwise.
	//   - "Programmed": True when at least one backing proxy is active. Per-Gateway
	//     health is in Proxies.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Proxies reports the health of each AWS proxy backing this ELBProxyConfiguration,
	// one entry per target Gateway (a merged GatewayClass has one entry).
	//
	// Atomic because the key, gatewayRef, is a struct and a listMapKey must be scalar.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	Proxies []ProxyStatusEntry `json:"proxies,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed ELBProxyConfiguration
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ProxyStatusEntry reports the health of one AWS proxy backing this ELBProxyConfiguration
// for a specific target Gateway.
type ProxyStatusEntry struct {
	// GatewayRef identifies the Gateway this entry describes.
	// +kubebuilder:validation:Required
	GatewayRef GatewayReference `json:"gatewayRef"`

	// ProxyArn is the ARN of the AWS proxy backing this Gateway. Empty until known.
	// +optional
	ProxyArn string `json:"proxyArn,omitempty"`

	// Conditions describe the health of this proxy.
	//
	// Known condition types:
	//   - "Programmed": True when the proxy is active. False with reason Pending
	//     (being created), ActionRequired (the AWS status reasons are in the message),
	//     CreateFailed, or Deleting. After Deleting, the entry is removed on Gateway
	//     deletion, or moves to Pending on replacement.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GatewayReference names a Gateway that an ELBProxyConfiguration backs.
// A merged GatewayClass uses Kind "GatewayClass" and the class name.
type GatewayReference struct {
	// Kind of the referent: "GatewayClass" for a merged gateway, else "Gateway".
	// +optional
	// +kubebuilder:default="Gateway"
	// +kubebuilder:validation:Enum=Gateway;GatewayClass
	Kind string `json:"kind,omitempty"`

	// Namespace of the referent. Empty for a merged GatewayClass (cluster-scoped).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name of the referent Gateway (or GatewayClass name for a merged gateway).
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// +kubebuilder:object:root=true

// ELBProxyConfigurationList contains a list of ELBProxyConfiguration
type ELBProxyConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ELBProxyConfiguration `json:"items"`
}
