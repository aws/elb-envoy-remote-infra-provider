# Subnet discovery

This page explains how the controller chooses the subnets for each proxy, and how to control that choice. Each proxy runs in at least two subnets, one for each Availability Zone, in the cluster's virtual private cloud (VPC). You can list the subnets yourself in the `ELBProxyConfiguration`, or let the controller discover them.

## How the controller picks subnets

For each proxy, the controller uses the first of these that applies:

1. **Subnets in the spec.** If `spec.networkConfiguration.vpcConfiguration.subnets` is set, it uses exactly those. See [Specify subnets](#specify-subnets).
2. **The existing proxy's subnets.** If the proxy already exists, it keeps the subnets the proxy has now. Discovery doesn't run again, so later tag changes don't move an existing proxy.
3. **Discovery.** Otherwise, it discovers subnets as described below.

## Discovery

### Candidate subnets

The controller looks for subnets in the cluster's VPC with the **role tag** that matches the proxy's addressability. The following table shows each role tag.

| Addressability | Role tag |
|----------------|----------|
| `internet-facing` | `kubernetes.io/role/elb` |
| `internal` | `kubernetes.io/role/internal-elb` |

The tag's value must be `1` or empty. These are the same tags the AWS Load Balancer Controller uses, so subnets already tagged for it work without changes.

Addressability comes from `ipv4Addressability`. For an IPv6-only proxy (`ipv4Addressability: off`), it comes from `ipv6Addressability` instead. See [Addressability](../guide/overview.md#addressability) for the defaults.

**If no subnet has the role tag,** the controller falls back to route tables. A subnet whose route table has a route to an internet gateway counts as public, and is a candidate for internet-facing proxies. Any other subnet counts as private, and is a candidate for internal proxies. A subnet without its own route table uses the VPC's main route table.

### Filtering

From the candidates, the controller skips a subnet if:

- It's tagged `kubernetes.io/cluster/<name>` only for **other** clusters. Subnets with no cluster tag, or with this cluster's tag, are kept.
- It has fewer than **8** available IP addresses.
- The proxy has IPv6 enabled and the subnet has no associated IPv6 CIDR block.

### Final selection

The controller then picks **one subnet per Availability Zone**:

- A subnet tagged `kubernetes.io/cluster/<this cluster>` wins over an untagged one.
- Otherwise, the subnet with the lowest subnet ID wins.

Subnets in Local Zones, Wavelength Zones, and Outposts are dropped. The result must have **at least two subnets**. If it doesn't, the Gateway gets a `FailedDeployModel` event. If no subnet is eligible, the event says how many subnets were skipped, and why. If only one remains, it says the count is below the minimum of two. See [FailedDeployModel errors](../troubleshooting/index.md#faileddeploymodel-errors).

### Tag your subnets

Tag at least two subnets, in different Availability Zones, for each kind of proxy you create:

```bash
# Subnets for internet-facing proxies
aws ec2 create-tags --resources subnet-0123456789abcdef0 subnet-0fedcba9876543210 \
  --tags Key=kubernetes.io/role/elb,Value=1

# Subnets for internal proxies
aws ec2 create-tags --resources subnet-0aaaaaaaaaaaaaaaa subnet-0bbbbbbbbbbbbbbbb \
  --tags Key=kubernetes.io/role/internal-elb,Value=1
```

If several clusters share the VPC, also tag each subnet `kubernetes.io/cluster/<cluster-name>=shared` for the clusters that may use it. The controller skips a subnet tagged only for other clusters.

## Specify subnets

To choose subnets yourself, list them in the `ELBProxyConfiguration`:

```yaml
spec:
  networkConfiguration:
    vpcConfiguration:
      subnets:
        - subnetId: subnet-0123456789abcdef0
        - subnetId: subnet-0fedcba9876543210
```

The list must have:

- At least two subnets, each in a different Availability Zone.
- Only standard Availability Zones. A subnet in a Local Zone, Wavelength Zone, or Outpost is rejected.
- An associated IPv6 CIDR block on every subnet, when the proxy has IPv6 enabled.

`subnetId` also accepts a subnet's `Name` tag value instead of its ID. A name has to match a subnet in the cluster's VPC. The controller doesn't check the VPC for IDs, so list only subnets from the cluster's VPC.

Role tags, cluster tags, and free IP addresses aren't checked for subnets you list. Make sure they suit the proxy's addressability: for example, internet-facing proxies need public subnets.

## Change subnets on an existing proxy

- **Edit `subnets`:** the controller updates the proxy's subnets in place. The proxy and its address stay the same.
- **Remove `subnets`:** the proxy keeps the subnets it has now. Discovery doesn't run again.
- **Move a discovered proxy to other subnets:** retagging subnets has no effect on an existing proxy. List the new subnets in `subnets` instead.
- **Change addressability:** a change to `ipConfiguration`, or to the `EnvoyProxy`'s `ipFamily` when `ipConfiguration` is unset, replaces the proxy, and the new proxy gets a new address. Because the role tag depends on addressability, list `subnets` explicitly when you make this change. This ensures the new proxy lands in subnets that suit it.
