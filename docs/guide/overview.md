# ELBProxyConfiguration

`ELBProxyConfiguration` tells the controller to run a Gateway on an AWS managed proxy, and how to configure that proxy. It's a [Gateway API policy](https://gateway-api.sigs.k8s.io/geps/gep-713/): it attaches to Gateways or GatewayClasses through `spec.targetRefs`.

A Gateway without an `ELBProxyConfiguration` has no AWS proxy.

!!! note
    The API version is `v1alpha1`. Check the release notes before you upgrade.

For every field, see the [Specification](spec.md).

## Attaching to Gateways

### Target a Gateway

Put the `ELBProxyConfiguration` in the **same namespace** as the Gateway:

```yaml
apiVersion: gateway.elbproxy.aws/v1alpha1
kind: ELBProxyConfiguration
metadata:
  name: my-proxy
  namespace: my-app
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: my-gateway
  roleArn: arn:aws:iam::123456789012:role/my-proxy-role
  networkConfiguration: {}
```

### Target a GatewayClass

To apply one configuration to every Gateway in a class, target the GatewayClass. The `ELBProxyConfiguration` must live in the **Envoy Gateway namespace** (by default `envoy-gateway-system`):

```yaml
apiVersion: gateway.elbproxy.aws/v1alpha1
kind: ELBProxyConfiguration
metadata:
  name: class-default
  namespace: envoy-gateway-system
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: GatewayClass
      name: elb-proxy-aws
  roleArn: arn:aws:iam::123456789012:role/my-proxy-role
  networkConfiguration: {}
```

Each Gateway in the class still has its own proxy, unless you use [merged gateways](#merged-gateways).

If a GatewayClass-scoped `ELBProxyConfiguration` is in any other namespace, it's ignored and its status shows `Accepted=False` with reason `InvalidTargetRef`.

### Precedence

When several `ELBProxyConfiguration`s match one Gateway:

1. A Gateway-scoped one takes precedence over a GatewayClass-scoped one.
2. Within the same scope, the oldest takes precedence (by creation time, then namespace, then name).

The others show `Accepted=False` with reason `Conflicted`.

### Merged gateways

With Envoy Gateway's merged gateways mode, all Gateways in a class share **one proxy**, and so one address. Only GatewayClass-scoped `ELBProxyConfiguration`s apply in this mode. Gateway-scoped ones are ignored.

To use it, set `mergeGateways: true` on the `EnvoyProxy` that the GatewayClass references, and target the GatewayClass from an `ELBProxyConfiguration` in the Envoy Gateway namespace. The controller then creates one proxy for the class, with one frontend security group that allows the listener ports of all the Gateways. Events for the proxy appear on every Gateway in the class.

For a complete walkthrough with two Gateways on one proxy, see [Merged Gateways](../examples/merged_gateways.md).

## Networking

### Addressability

`spec.networkConfiguration.ipConfiguration.ipAddressingConfiguration` sets whether the proxy is reachable from the internet or only inside the virtual private cloud (VPC). Each of `ipv4Addressability` and `ipv6Addressability` can be `off`, `internal`, or `internet-facing`.

If you don't set them, the defaults come from the `spec.ipFamily` field of the `EnvoyProxy`. The following table shows the defaults.

| `ipFamily` | IPv4 | IPv6 |
|------------|------|------|
| `IPv4` (default) | `internal` | `off` |
| `IPv6` | `off` | `internal` |
| `DualStack` | `internal` | `internal` |

!!! warning
    - The IP configuration can't be changed on an existing proxy. If you change it, the controller **replaces** the proxy. The same happens if you change the `ipFamily` field of the `EnvoyProxy` while you haven't set `ipv4Addressability` and `ipv6Addressability` explicitly. The controller deletes the old proxy and creates a new one with a new address. See [Change subnets on an existing proxy](../deploy/subnet_discovery.md#change-subnets-on-an-existing-proxy).
    - Quote `"off"` in YAML. Unquoted, it's read as the boolean `false`.
    - The API rejects these combinations: both `off`, IPv4 `internet-facing` with IPv6 `internal`, and IPv4 `internal` with IPv6 `internet-facing`.

### Subnets

If you don't set `spec.networkConfiguration.vpcConfiguration.subnets`, the controller discovers subnets in the cluster's VPC with the `kubernetes.io/role/elb` (internet-facing) or `kubernetes.io/role/internal-elb` (internal) tag, one for each Availability Zone, at least two. An existing proxy keeps its subnets. See [Subnet discovery](../deploy/subnet_discovery.md) for the full rules, and how to tag subnets.

To choose subnets yourself, list at least two, in different Availability Zones, in the same VPC:

```yaml
spec:
  networkConfiguration:
    vpcConfiguration:
      subnets:
        - subnetId: subnet-0123456789abcdef0
        - subnetId: subnet-0fedcba9876543210
```

When IPv6 is enabled, the subnets need IPv6 CIDR blocks.

### Security groups

By default, the controller creates and manages a frontend security group for the proxy. It allows inbound traffic on each listener port from:

- `spec.ingressCidrs`: IPv4 or IPv6 CIDRs. Defaults to `0.0.0.0/0`, plus `::/0` when the proxy has IPv6 enabled.
- `spec.ingressPrefixLists`: AWS prefix list IDs.

```yaml
spec:
  ingressCidrs:
    - 10.0.0.0/8
  ingressPrefixLists:
    - pl-0123456789abcdef0
```

The controller removes inbound rules it didn't create, so don't add rules to this security group manually.

To use your own security groups, set `spec.networkConfiguration.vpcConfiguration.securityGroups`. The controller then doesn't manage any security group rules. See [Security groups](../deploy/security_groups.md) for both modes, what you need to allow, and switching between them.

## Other settings

The following table describes the other settings you can set.

| Field | Description |
|-------|-------------|
| `roleArn` | Required. AWS Identity and Access Management (IAM) role the proxy uses when Envoy filters or extensions call AWS services. Changing it replaces the proxy, which changes the Gateway's address. |
| `name` | Proxy name, up to 32 characters. Generated if unset. Changing it creates a new proxy and deletes the old one, which changes the Gateway's address. |
| `attributes` | Proxy attributes as key-value pairs. See [Attributes](#attributes). |
| `tags` | AWS tags on the proxy. These take precedence over `--default-tags`. Don't use keys that start with `gateway.elbproxy.aws/`. |
| `customerKmsKeyArn` | Customer managed KMS key used to encrypt the proxy's configuration. Defaults to a key that the service owns. See [Customer managed KMS keys](integrations.md#customer-managed-kms-keys). |

### Attributes

The controller passes each entry in `spec.attributes` to ELB for Envoy without checking the key, so any attribute the service supports works. For the full list, see the ELB for Envoy API reference. The following table lists attributes that this page covers.

| Key | Description |
|-----|-------------|
| `deletion_protection.enabled` | When `true`, the proxy can't be deleted. Default `false`. |
| `waf.web_acl_arn` | ARN of your own AWS WAF web ACL to protect the proxy. Set it when the proxy is created. To change or remove it later, use the AWS WAF API. See [AWS WAF](integrations.md#aws-waf). |
| `waf.fail_open.enabled` | When `true`, the proxy lets requests through if WAF is unavailable. Default `false`. |

```yaml
spec:
  attributes:
    - key: waf.web_acl_arn
      value: arn:aws:wafv2:us-west-2:111122223333:regional/webacl/my-acl/a1b2c3d4-5678-90ab-cdef-EXAMPLE11111
```

How the controller applies attributes:

- It updates a changed attribute on the existing proxy.
- If you remove an attribute from the spec, the controller resets it to its default value, when it has one.
- An attribute that can't be changed after creation, such as `waf.web_acl_arn`, fails with an error if you change it. The error appears as a `FailedDeployModel` event on the Gateway.
- The controller sets the Envoy log level from `EnvoyProxy.spec.logging.level`, so it rejects `envoy.log_level` and `envoy.component_log_level` here.

## EnvoyProxy requirements

The controller reads the `EnvoyProxy` that Envoy Gateway uses for the Gateway. Disable access logging, or send it to a network sink such as OpenTelemetry or the Access Log Service (ALS):

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
spec:
  telemetry:
    accessLog:
      disable: true
```

The controller rejects a file sink, including Envoy Gateway's default `/dev/stdout`, because the path doesn't exist on the managed proxy.

## Status and events

### Conditions

The `ELBProxyConfiguration` has two conditions.

`Accepted` says whether it applies to any Gateway. The following table lists its statuses and reasons.

| Status | Reason | Meaning |
|--------|--------|---------|
| `True` | `Accepted` | Applied to at least one Gateway. |
| `False` | `Conflicted` | Another `ELBProxyConfiguration` wins for the same target. |
| `False` | `InvalidTargetRef` | Targets a GatewayClass but isn't in the Envoy Gateway namespace. |

`Programmed` summarizes the AWS proxies behind it. It's `True` when **at least one** of them is active, with a message such as `2 of 3 backing proxies are active`. When it targets several Gateways, check each one in `status.proxies`.

### Per-proxy status

`status.proxies` has one entry per target Gateway. A [merged GatewayClass](#merged-gateways) has a single entry with `kind: GatewayClass`. Each entry has the proxy's Amazon Resource Name (ARN) and its own `Programmed` condition:

```yaml
status:
  proxies:
    - gatewayRef:
        kind: Gateway
        namespace: default
        name: my-gateway
      proxyArn: arn:aws:elasticloadbalancing:us-west-2:111122223333:proxy/elbp-...
      conditions:
        - type: Programmed
          status: "False"
          reason: Pending
          message: Proxy is being created
```

The following table lists the possible values of the per-proxy `Programmed` condition.

| Status | Reason | Meaning |
|--------|--------|---------|
| `True` | `Programmed` | The proxy is active. |
| `False` | `Pending` | The proxy is being created. |
| `False` | `ActionRequired` | The proxy needs action. The message lists the reasons from AWS, in the form `<code>: <message> [<target>]`. |
| `False` | `CreateFailed` | The proxy failed to create. |
| `False` | `Deleting` | The proxy is being deleted, because the Gateway was deleted, or because it is being replaced and the new proxy has the same name (you set `spec.name`). |

While the controller updates a proxy, its entry keeps its last condition. When you delete the Gateway, the controller removes its entry once the proxy is gone. When a proxy is replaced under the same name, the entry shows `Deleting` until the old proxy is gone, then `Pending` for the new proxy. When the replacement has a different name, the controller creates the new proxy right away. An `ELBProxyConfiguration` reports at most 32 proxies; past that, the controller logs `status.proxies is full` and doesn't add more entries.

```bash
kubectl get elbproxyconfiguration <name> -n <namespace> \
  -o jsonpath='{range .status.proxies[*]}{.gatewayRef.namespace}/{.gatewayRef.name}{"  "}{.conditions[?(@.type=="Programmed")].reason}{": "}{.conditions[?(@.type=="Programmed")].message}{"\n"}{end}'
```

### Events

The controller also writes events on the Gateway. The following table lists them.

| Reason | Type | Meaning |
|--------|------|---------|
| `SuccessfullyReconciled` | Normal | The controller applied the configuration to AWS. It doesn't mean the proxy is active; check [per-proxy status](#per-proxy-status). |
| `FailedDeployModel` | Warning | The controller couldn't create or update the proxy. The message has the error. |
| `WaitingForProxyDeletion` | Normal | A previous proxy is still being deleted. The new one is created after it's gone. |

```bash
kubectl describe gateway <name> -n <namespace>
```
