# Compatibility

## Version matrix

The following table lists the versions this controller release supports.

| Controller | Envoy Gateway | Envoy Proxy | Gateway API | Kubernetes |
|------------|---------------|-------------------------------------|-------------|------------|
| v1.0.0 | v1.10 | 1.40 | v1.6.1 | v1.34, v1.35, v1.36, v1.37 |

- **Envoy Gateway:** the version this controller release is built and tested against. Envoy Gateway must be configured as a remote infrastructure provider (`infrastructure.type: Remote`).
- **Envoy Proxy:** the Envoy minor version that the Envoy Gateway release in that row generates configuration for. The controller creates every proxy with this version, taken from the Envoy Gateway version it's built against, not the one running in your cluster. You can't choose a different version, and the controller doesn't change the version of an existing proxy.
- **Gateway API and Kubernetes:** follow the Envoy Gateway release. The controller also requires Kubernetes 1.29 or later for native sidecars, which every version in this range meets.

The Envoy Proxy, Gateway API, and Kubernetes versions for each Envoy Gateway release come from the [Envoy Gateway compatibility matrix](https://gateway.envoyproxy.io/news/releases/matrix/).

## Upgrading

Upgrade Envoy Gateway and the controller together, to the versions in the same row of the [version matrix](#version-matrix). Each Envoy Gateway release generates configuration for a single Envoy minor version, and the controller creates proxies with the Envoy version of the Envoy Gateway release it's built against. A newer Envoy Gateway than the matrix lists can generate configuration for an Envoy version that your proxies don't run. This combination isn't supported.

Upgrading the controller doesn't change the Envoy version of existing proxies. A proxy keeps the version it was created with.

## Envoy Gateway features

The proxy runs the configuration Envoy Gateway generates, so Gateway API and Envoy Gateway features work as described in the [Envoy Gateway documentation](https://gateway.envoyproxy.io/docs/), for the Envoy Gateway version in the matrix above.

Because the proxy runs in AWS instead of in your cluster, a few things differ. The following table lists them.

| Feature | Difference |
|---------|------------|
| `EnvoyProxy` settings | The controller doesn't pass `provider.kubernetes` (replicas, autoscaling, disruption budgets, Service type and annotations, pod patches), `concurrency`, `extraArgs`, or `shutdown` to the proxy, because AWS runs and scales it. Other `EnvoyProxy` settings, such as `routingType` and `filterOrder`, change the xDS configuration that Envoy Gateway generates, so they still apply. |
| Access logs | Must be disabled or use an OpenTelemetry or Access Log Service (ALS) sink. The controller rejects file sinks, including the default `/dev/stdout`. See [Observability](guide/observability.md). |
| Global rate limiting | Not supported. Envoy Gateway doesn't deploy its rate limit service with a remote infrastructure provider, and it points proxies at that service's cluster DNS name, which doesn't resolve outside the cluster. A `BackendTrafficPolicy` with `type: Global` either isn't enforced or fails every request on its routes. Which one happens depends on `failClosed`. Use local rate limiting instead. See [Global rate limiting doesn't work](troubleshooting/index.md#global-rate-limiting-doesnt-work). |
| Local rate limiting | Supported. `type: Local` needs no extra service. Each proxy instance counts separately, so the effective limit is the configured limit times the number of instances serving traffic. |
| Merged gateways | One AWS proxy per GatewayClass. |
| Standalone (non-Kubernetes) Envoy Gateway | Not supported. |

## Platform requirements

- A Kubernetes cluster whose nodes are Amazon EC2 instances in the same virtual private cloud (VPC) as the proxy, such as an Amazon EKS cluster. The controller finds each node's EC2 instance from the node's `spec.providerID`, so the nodes need an AWS provider ID, which the AWS cloud provider sets. On a cluster that isn't Amazon EKS, also tag the node security groups `kubernetes.io/cluster/<cluster-name>`, with the value `owned` or `shared`.
- Gateway API CRDs from the standard channel, v1.6 or later, including `TCPRoute`, `TLSRoute`, `UDPRoute`, and `ListenerSet` at `gateway.networking.k8s.io/v1`. The controller watches all of them and doesn't skip one whose CRD is missing, so install them all, even if you don't use every kind.
- AWS credentials for the Envoy Gateway pod, from any source that the default credential chain of the AWS SDK supports. The [Installation Guide](deploy/installation.md#step-2-grant-the-policy-to-the-envoy-gateway-service-account) shows EKS Pod Identity and IAM roles for service accounts (IRSA).
- Pod IPs routable in the VPC, for example with the Amazon VPC CNI. The proxy connects directly to Envoy Gateway and backend pod IPs. Overlay networks, such as Calico with VXLAN or flannel, don't meet this requirement. If your pods have their own security groups, add the security group rules for them yourself. See [Node security group rules](deploy/security_groups.md#node-security-group-rules).
- Envoy Gateway's leader election disabled. See [How it works](how-it-works.md#high-availability-and-leader-election).
