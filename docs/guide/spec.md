# API Reference

## Packages
- [gateway.elbproxy.aws/v1alpha1](#gatewayelbproxyawsv1alpha1)


## gateway.elbproxy.aws/v1alpha1


### Resource Types
- [ELBProxyConfiguration](#elbproxyconfiguration)



#### Attribute







_Appears in:_
- [ELBProxyConfigurationSpec](#elbproxyconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `key` _string_ |  |  |  |
| `value` _string_ |  |  |  |


#### ELBProxyConfiguration



ELBProxyConfiguration is a CRD for ELB for Envoy proxy configuration.

ELBProxyConfiguration is a direct Gateway API policy: it attaches to one or more Gateway
or GatewayClass resources via spec.targetRefs. When a Gateway is targeted, the
ELBProxyConfiguration must live in the same namespace as the Gateway.

When a GatewayClass is targeted, the ELBProxyConfiguration must live in the controller's own namespace
GatewayClass-scope targetRefs from ELBProxyConfigurations in any other namespace are ignored.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `gateway.elbproxy.aws/v1alpha1` | | |
| `kind` _string_ | `ELBProxyConfiguration` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ELBProxyConfigurationSpec](#elbproxyconfigurationspec)_ |  |  |  |
| `status` _[ELBProxyConfigurationStatus](#elbproxyconfigurationstatus)_ |  |  |  |


#### ELBProxyConfigurationSpec







_Appears in:_
- [ELBProxyConfiguration](#elbproxyconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `targetRefs` _LocalPolicyTargetReference array_ | TargetRefs are the Gateway or GatewayClass resources this ELBProxyConfiguration<br />attaches to.<br />Only group "gateway.networking.k8s.io" with kind "Gateway" or<br />"GatewayClass" are accepted. Gateway targets resolve within the<br />ELBProxyConfiguration's own namespace; GatewayClass targets are honored only<br />when the ELBProxyConfiguration lives in the controller's own namespace<br />When multiple ELBProxyConfigurations resolve to the same Gateway, a Gateway-scoped<br />attachment overrides a GatewayClass-scoped one. Ties within the same<br />scope are broken by creationTimestamp, then namespace, then name. |  | MaxItems: 16 <br />MinItems: 1 <br /> |
| `name` _string_ | Name of the proxy. If unspecified, it is generated automatically.<br />Changing it replaces the proxy: the controller deletes it and creates a new one. |  | MaxLength: 32 <br />MinLength: 1 <br />Pattern: `^[a-zA-Z0-9]+$\|^[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9]$` <br /> |
| `roleArn` _string_ | The Amazon Resource Name (ARN) of the IAM role that provides permissions to<br />the code that your Envoy configuration runs on the proxy, such as filters and<br />extensions. These are the permissions that this code needs when it calls AWS<br />services.<br />Changing it replaces the proxy: the controller deletes it and creates a new one. |  | MaxLength: 2048 <br />MinLength: 20 <br />Pattern: `^arn:[a-z-]+:iam::[0-9]\{12\}:role/.+$` <br />Required: \{\} <br /> |
| `attributes` _[Attribute](#attribute) array_ | Attributes for proxy configuration. Each key can appear only once. |  |  |
| `networkConfiguration` _[ProxyNetworkUserConfiguration](#proxynetworkuserconfiguration)_ | NetworkConfiguration for VPC and IP settings |  |  |
| `ingressCidrs` _string array_ | IngressCidrs restricts which CIDR ranges are allowed to reach the proxy's<br />frontend security group. Applies to all listener ports.<br />Each entry must be a valid IPv4 CIDR or IPv6 CIDR<br />If unset, defaults to ["0.0.0.0/0", "::/0"] — open to all traffic.<br />Only applies when the controller manages the frontend SG (i.e. when<br />spec.networkConfiguration.vpcConfiguration.securityGroups is not set). |  | MaxItems: 100 <br /> |
| `ingressPrefixLists` _string array_ | IngressPrefixLists specifies AWS prefix list IDs whose members are allowed<br />to reach the proxy's frontend security group. Applies to all listener ports.<br />Each entry must be a valid AWS prefix list ID (e.g. "pl-0123456789abcdef0").<br />Only applies when the controller manages the frontend SG (i.e. when<br />spec.networkConfiguration.vpcConfiguration.securityGroups is not set). |  | MaxItems: 30 <br /> |
| `tags` _object (keys:string, values:string)_ | Tags defines list of Tags on the Proxy. |  |  |
| `customerKmsKeyArn` _string_ | CustomerKmsKeyArn is the ARN of a customer-managed KMS key (CMK) used to<br />encrypt the proxy's configuration content. If unset, the default<br />service-owned key is used. |  |  |


#### ELBProxyConfigurationStatus



ELBProxyConfigurationStatus defines the observed state of ELBProxyConfiguration



_Appears in:_
- [ELBProxyConfiguration](#elbproxyconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#condition-v1-meta) array_ | Conditions describe the ELBProxyConfiguration.<br />Known condition types:<br />  - "Accepted": True when this is the winning policy for at least one target<br />    Gateway; False/Conflicted or False/InvalidTargetRef otherwise.<br />  - "Programmed": True when at least one backing proxy is active. Per-Gateway<br />    health is in Proxies. |  |  |
| `proxies` _[ProxyStatusEntry](#proxystatusentry) array_ | Proxies reports the health of each AWS proxy backing this ELBProxyConfiguration,<br />one entry per target Gateway (a merged GatewayClass has one entry).<br />Atomic because the key, gatewayRef, is a struct and a listMapKey must be scalar. |  | MaxItems: 32 <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed ELBProxyConfiguration |  |  |


#### GatewayReference



GatewayReference names a Gateway that an ELBProxyConfiguration backs.
A merged GatewayClass uses Kind "GatewayClass" and the class name.



_Appears in:_
- [ProxyStatusEntry](#proxystatusentry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind of the referent: "GatewayClass" for a merged gateway, else "Gateway". | Gateway | Enum: [Gateway GatewayClass] <br /> |
| `namespace` _string_ | Namespace of the referent. Empty for a merged GatewayClass (cluster-scoped). |  |  |
| `name` _string_ | Name of the referent Gateway (or GatewayClass name for a merged gateway). |  | Required: \{\} <br /> |


#### ProxyIpAddressingConfiguration







_Appears in:_
- [ProxyIpConfiguration](#proxyipconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipv4Addressability` _string_ | Ipv4Addressability defines IPv4 addressing mode.<br />If unset, defaults are derived from the EnvoyProxy IPFamily. "internal" is the default. |  | Enum: [off internal internet-facing] <br /> |
| `ipv6Addressability` _string_ | Ipv6Addressability defines IPv6 addressing mode.<br />If unset, defaults are derived from the EnvoyProxy IPFamily. "off" is the default for IPv4-only. |  | Enum: [off internal internet-facing] <br /> |


#### ProxyIpConfiguration







_Appears in:_
- [ProxyNetworkUserConfiguration](#proxynetworkuserconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipAddressingConfiguration` _[ProxyIpAddressingConfiguration](#proxyipaddressingconfiguration)_ | IpAddressingConfiguration defines IPv4 and IPv6 addressability settings.<br />If unset, defaults are derived from the EnvoyProxy IPFamily:<br />  IPv4 (default): ipv4=internal, ipv6=off<br />  IPv6:           ipv4=off,      ipv6=internal<br />  DualStack:      ipv4=internal, ipv6=internal<br />"internal" (default) means VPC-only; use "internet-facing" to expose publicly.<br />The ELB Proxy API rejects these combinations: both off; IPv4 internet-facing<br />with IPv6 internal; and IPv4 internal with IPv6 internet-facing. Quote "off"<br />in YAML, since an unquoted off is parsed as the boolean false. |  |  |


#### ProxyNetworkUserConfiguration







_Appears in:_
- [ELBProxyConfigurationSpec](#elbproxyconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `vpcConfiguration` _[ProxyVpcUserConfiguration](#proxyvpcuserconfiguration)_ | VpcConfiguration defines VPC-specific settings including subnets and security groups. |  |  |
| `ipConfiguration` _[ProxyIpConfiguration](#proxyipconfiguration)_ | IpConfiguration defines IP addressing settings for IPv4 and IPv6.<br />Changing it replaces the proxy: the controller deletes it and creates a new one. |  |  |


#### ProxyStatusEntry



ProxyStatusEntry reports the health of one AWS proxy backing this ELBProxyConfiguration
for a specific target Gateway.



_Appears in:_
- [ELBProxyConfigurationStatus](#elbproxyconfigurationstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `gatewayRef` _[GatewayReference](#gatewayreference)_ | GatewayRef identifies the Gateway this entry describes. |  | Required: \{\} <br /> |
| `proxyArn` _string_ | ProxyArn is the ARN of the AWS proxy backing this Gateway. Empty until known. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#condition-v1-meta) array_ | Conditions describe the health of this proxy.<br />Known condition types:<br />  - "Programmed": True when the proxy is active. False with reason Pending<br />    (being created), ActionRequired (the AWS status reasons are in the message),<br />    CreateFailed, or Deleting. After Deleting, the entry is removed on Gateway<br />    deletion, or moves to Pending on replacement. |  |  |


#### ProxyVpcUserConfiguration







_Appears in:_
- [ProxyNetworkUserConfiguration](#proxynetworkuserconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `subnets` _[SubnetDetails](#subnetdetails) array_ | Subnets for the proxy. If unspecified, the controller will use subnet Auto-Discovery.<br />When set, provide at least 2 subnets, each in a different Availability Zone,<br />all in the same VPC. |  | MinItems: 2 <br /> |
| `securityGroups` _[SecurityGroupDetails](#securitygroupdetails) array_ | SecurityGroups for the proxy. If unspecified, the controller will use default security groups. |  |  |


#### SecurityGroupDetails







_Appears in:_
- [ProxyVpcUserConfiguration](#proxyvpcuserconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `securityGroupId` _string_ |  |  |  |


#### SubnetDetails







_Appears in:_
- [ProxyVpcUserConfiguration](#proxyvpcuserconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `subnetId` _string_ |  |  |  |


