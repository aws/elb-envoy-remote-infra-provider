# Security groups

The proxy runs in your virtual private cloud (VPC) and needs two kinds of network access: clients reaching its listeners, and the proxy reaching Envoy Gateway and your pods. By default, the controller manages the security groups for both. You can also bring your own.

The following table compares the two modes.

| Mode | Set by | The controller manages |
|------|--------|------------------------|
| [Controller-managed](#controller-managed-security-groups) (default) | `vpcConfiguration.securityGroups` unset | A frontend security group per proxy, a shared backend security group, and rules on your node security groups |
| [Your own](#bring-your-own-security-groups) | `vpcConfiguration.securityGroups` set | Nothing. You manage every rule. |

## Controller-managed security groups

```
clients --listener ports--> [frontend SG] proxy [backend SG] --18000, 18002, backend ports--> [node SG] Envoy Gateway pods, backend pods
```

### Frontend security group

One per proxy: per Gateway, or per GatewayClass for [merged gateways](../guide/overview.md#merged-gateways). It allows inbound traffic to the proxy's listeners:

- **One rule per listener port and protocol.** TCP for TCP-based listeners and UDP for UDPRoute listeners. For an HTTP/3 listener, the HTTPS port also gets a UDP rule for QUIC.
- **Sources:**
    - `spec.ingressCidrs`. IPv6 CIDRs are dropped when the proxy has IPv6 off.
    - If `ingressCidrs` is unset: `0.0.0.0/0`, plus `::/0` when the proxy has IPv6 enabled. For an internal proxy, that means anyone in the VPC or connected networks.
    - `spec.ingressPrefixLists`, in addition to the CIDRs.

```yaml
spec:
  ingressCidrs:
    - 10.0.0.0/8
  ingressPrefixLists:
    - pl-0123456789abcdef0
```

When you change `ingressCidrs`, `ingressPrefixLists`, or the Gateway's listeners, the controller updates the rules in place. The security group and the proxy stay the same.

!!! warning "Rules you add by hand are removed"
    The controller keeps the frontend security group's inbound rules exactly in sync with the listeners and sources above, and removes any other rule on the next reconcile. To open more ports, such as `19001` for Prometheus, [bring your own security groups](#bring-your-own-security-groups).

The controller doesn't manage outbound rules. A new security group allows all outbound traffic.

### Backend security group

One per controller installation (cluster name and controller namespace), attached to every proxy that installation manages. It has no rules of its own. It's the source for the node security group rules below, so one set of node rules covers every proxy.

The controller creates it when the first managed proxy needs it, and deletes it when no Gateway does.

### Node security group rules

The controller adds inbound rules to your node security groups, with the backend security group as the source:

- TCP 18000, so the proxy can get its configuration from Envoy Gateway over xDS.
- TCP 18002, so the proxy can download Wasm modules from Envoy Gateway.
- Every backend target port, with its protocol, that the Gateways this installation manages send traffic to: route backends, request mirror targets, and the `ext_authz` and `ext_proc` services of your `SecurityPolicy` and `EnvoyExtensionPolicy` resources. The ports for mirror targets and policy services use TCP.

The controller adds the two Envoy Gateway ports as soon as a Gateway exists, even before a route does, and removes all of these rules when the last Gateway is deleted. It labels each rule with its installation and adds or removes only its own rules, so multiple installations and other tools can share a node security group.

**Which node security groups.** For each Ready EC2 node, the controller uses the security group on the node's primary network interface. If that interface has several security groups, it uses the one tagged `kubernetes.io/cluster/<cluster-name>`, and there must be exactly one. The controller AWS Identity and Access Management (IAM) policy only allows rule changes on security groups with that tag, so the node security group needs it. The EKS cluster security group has it by default. When the last Gateway is deleted, the controller removes its rules from every security group in the VPC that has this tag.

!!! note "Pods with their own security groups"
    The controller adds rules only to node security groups. If your pods have their own security groups, for example with security groups for pods or with custom networking on the Amazon VPC CNI, the controller doesn't change those groups, and the node security group rules above don't apply to the pods' network interfaces. Add the following inbound rules yourself, with the backend security group as the source, or your own security groups if you [bring your own](#bring-your-own-security-groups):

    - TCP 18000 on the security group of the Envoy Gateway pods, and TCP 18002 if you use Wasm extensions.
    - Each backend target port on the security group of the backend pods.

    Without them, the proxy gets no configuration from Envoy Gateway, or requests to your backends fail. Neither shows up as an error in the cluster. To find the backend security group, see [Find the security groups](#find-the-security-groups).

### Find the security groups

```bash
# Frontend SG of a Gateway
aws ec2 describe-security-groups \
  --filters Name=tag:gateway.elbproxy.aws/cluster,Values=$CLUSTER_NAME \
            Name=tag:gateway.elbproxy.aws/stack,Values=<gateway-namespace>/<gateway-name> \
            Name=tag:gateway.elbproxy.aws/resource,Values=FrontendSecurityGroup \
  --query 'SecurityGroups[].GroupId' --output text

# Backend SG
aws ec2 describe-security-groups \
  --filters Name=tag:gateway.elbproxy.aws/cluster,Values=$CLUSTER_NAME \
            Name=tag:gateway.elbproxy.aws/resource,Values=BackendSecurityGroup \
  --query 'SecurityGroups[].[GroupId,GroupName]' --output text
```

Frontend security groups are named `elbproxy-<cluster>-<namespace>-<gateway>-<hash>`, and the backend security group `elbproxy-backend-<cluster>-<hash>`, with each part shortened.

## Bring your own security groups

List your security groups in the `ELBProxyConfiguration`:

```yaml
spec:
  networkConfiguration:
    vpcConfiguration:
      securityGroups:
        - securityGroupId: sg-0123456789abcdef0
```

The proxy then uses exactly these security groups, and the controller:

- doesn't create a frontend or backend security group,
- doesn't add node security group rules,
- never changes or deletes your security groups,
- ignores `ingressCidrs` and `ingressPrefixLists`, and logs that it did.

You must allow the access in the following table yourself.

| Direction | Port | Between |
|-----------|------|---------|
| Inbound to the proxy | Each listener port | From your clients, on your security groups |
| Inbound to your nodes | TCP 18000 | From your security groups to the Envoy Gateway pods |
| Inbound to your nodes | TCP 18002 | From your security groups to the Envoy Gateway pods, if you use Wasm extensions |
| Inbound to your nodes | Each backend target port | From your security groups to the backend pods, and to any request mirror, `ext_authz`, or `ext_proc` services |
| Outbound from the proxy | All of the above, plus any [telemetry](#telemetry-ports) | On your security groups |

!!! warning
    Without the `18000` rule, the proxy gets no configuration from Envoy Gateway. Without the `18002` rule, the proxy can't download Wasm modules. Without the backend port rules, requests fail. Neither shows up as an error in the cluster.

When you change the list, the controller replaces the proxy's security groups in place.

## Switch between modes

**Controller-managed to your own.** In one reconcile, the controller switches the proxy to your security groups. It then deletes its old frontend security group. The proxy no longer has the backend security group, so the controller's node rules stop applying to it. Before switching, make sure your node security groups allow your security groups on TCP `18000` and the backend ports. Otherwise, the proxy loses its configuration and can't reach your pods.

**Your own to controller-managed.** Remove `securityGroups`. The controller creates the frontend and backend security groups, adds the node rules, and switches the proxy to them. Your security groups are detached but not changed or deleted.

## Telemetry ports

Ports for logs and metrics aren't opened in either mode:

- **To a collector in the cluster** (OpenTelemetry on `4317`): allow it on your node security group from the backend security group, or from your own security groups.
- **To the proxy's Prometheus port `19001`:** needs your own security groups, because the controller removes extra rules from the frontend security group.

See [Observability](../guide/observability.md).
