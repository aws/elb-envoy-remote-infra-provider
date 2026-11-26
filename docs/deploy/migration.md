# Migrating from in-cluster Envoy

This guide moves Gateways from Envoy Gateway's default in-cluster Envoy, with or without a Network Load Balancer (NLB) in front, to AWS managed Envoy proxies.

## Choose a migration approach

The remote infrastructure setting (`infrastructure.type: Remote`) applies to a whole Envoy Gateway install, not to single Gateways. Once an install is switched to remote, every Gateway it manages goes through `elb-envoy-remote-infra-provider`. A Gateway without an `ELBProxyConfiguration` gets no data plane at all.

The AWS proxy also has a new DNS name, so clients must move to a new address either way.

Because of this, use a side-by-side migration: run a second Envoy Gateway install configured as remote, create new Gateways on it, test them, and then move traffic with DNS. Your existing Gateways keep serving traffic until you cut over.

!!! warning "Don't switch an existing install in place"
    If you switch an install that serves traffic to remote, the Envoy Deployments, Services, and NLBs it created stay in the cluster, but they stop serving traffic. The Service still sends traffic to the port Envoy Gateway used for in-cluster Envoy (for example `10080` for a listener on port 80), and in remote mode Envoy listens on the listener's own port (`80`). The old Gateways keep their old addresses and still show `Programmed`. Rolling back the Helm release restores them.

## Before you migrate

Check your current setup for anything that doesn't carry over. The following table lists what to check.

| Check | What to do |
|-------|------------|
| Access logs go to a file or stdout (Envoy Gateway's default) | Switch to OpenTelemetry or the Access Log Service (ALS), or set `telemetry.accessLog.disable: true`. The controller rejects file sinks. |
| You use global rate limiting | Not supported. Switch the `BackendTrafficPolicy` to `type: Local`, which counts per proxy instance, or plan another alternative before migrating. See [Compatibility](../compatibility.md#envoy-gateway-features). |
| `EnvoyProxy` sets Kubernetes deployment settings (replicas, horizontal pod autoscaler (HPA), resources, pod or Service patches) | Not used. AWS scales the proxy. You can drop these settings. |
| Pod networking | Pod IPs must be routable in the virtual private cloud (VPC), for example with the Amazon VPC CNI. |
| Envoy Gateway leader election | Must be disabled on the remote install. See [How it works](../how-it-works.md#high-availability-and-leader-election). |
| You plan to use your own security groups | Allow traffic from the proxy to Envoy Gateway pods on TCP 18000, and on TCP 18002 if you use Wasm extensions, and to backend pods, request mirror targets, and `ext_authz` and `ext_proc` services on their target ports. |
| Kubernetes and Envoy Gateway versions | See [Compatibility](../compatibility.md). |

The proxy doesn't pass the client's source IP address to your backends. In `X-Forwarded-For` and `X-Envoy-External-Address`, backends see an AWS-owned address, not the client's. If your backends rely on the client IP, for example from NLB client IP preservation or proxy protocol, plan for this before you migrate.

Clients reach the proxy through its DNS name. ELB for Envoy doesn't support static IP addresses yet, such as an NLB's Elastic IP addresses. If your clients or partners allowlist IP addresses, don't migrate yet. See [NLB features that aren't supported yet](#nlb-features-that-arent-supported-yet).

## Mapping NLB settings

If you run an NLB in front of in-cluster Envoy today, usually created by the AWS Load Balancer Controller through annotations on the Envoy Service, map its settings to `ELBProxyConfiguration`. The following table shows the mapping.

| NLB + in-cluster Envoy | ELB for Envoy |
|------------------------|---------------|
| `service.beta.kubernetes.io/aws-load-balancer-scheme` | `networkConfiguration.ipConfiguration.ipAddressingConfiguration`: `internet-facing` or `internal`. Changing it later replaces the proxy, which gets a new address. |
| `service.beta.kubernetes.io/aws-load-balancer-ip-address-type: dualstack` | Set both `ipv4Addressability` and `ipv6Addressability`, or `EnvoyProxy.spec.ipFamily: DualStack`. |
| `service.beta.kubernetes.io/aws-load-balancer-subnets` | `networkConfiguration.vpcConfiguration.subnets` |
| Subnet discovery tags (`kubernetes.io/role/elb`, `kubernetes.io/role/internal-elb`) | The same tags. No change needed. |
| `loadBalancerSourceRanges` | `spec.ingressCidrs` |
| `service.beta.kubernetes.io/aws-load-balancer-security-groups` | `networkConfiguration.vpcConfiguration.securityGroups`, or leave unset to let the controller manage them. |
| `service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags` | `spec.tags`, or `--default-tags` for every proxy. |
| Envoy replicas and HPA | Not needed. |

See [ELBProxyConfiguration](../guide/overview.md) for every setting.

### NLB features that aren't supported yet

ELB for Envoy doesn't support the following NLB features yet, and they have no setting in `ELBProxyConfiguration`:

- Static IP addresses, such as Elastic IP addresses on the NLB.
- AWS PrivateLink, which exposes the NLB as a VPC endpoint service.
- AWS Global Accelerator, which uses the NLB as an endpoint.

!!! warning "Don't migrate if you depend on these"
    If your setup uses any of these features with the NLB, don't migrate yet. The new proxy's address can't be used in their place. Keep running in-cluster Envoy with the NLB until ELB for Envoy supports them.

## Step 1: Install a second Envoy Gateway

Install Envoy Gateway again in a **new namespace**, configured as a remote infrastructure provider, with its own `controllerName`. This example uses the namespace `envoy-gateway-elb` and the controller name `gateway.envoyproxy.io/elb-gatewayclass-controller`.

Add the sidecar the same way as in the [Installation Guide](installation.md#choose-how-to-add-the-sidecar). Use the same `$VERSION` of the controller as in the Installation Guide. Either way, the new install's `config` section looks like this:

```yaml
config:
  envoyGateway:
    gateway:
      # Different from your existing install, so each install only manages its own GatewayClass.
      controllerName: gateway.envoyproxy.io/elb-gatewayclass-controller
    provider:
      type: Custom
      custom:
        resource:
          type: Kubernetes
          kubernetes:
            leaderElection:
              disable: true
        infrastructure:
          type: Remote
          remote:
            service:
              unix:
                path: /var/run/elb-envoy-remote-infra-provider/server.sock
```

=== "Envoy Gateway Helm values (default)"

    Fill in `k8s/envoy-gateway-values.yaml` as in [Installation Guide Step 4](installation.md#step-4-install-envoy-gateway), then make two changes and save it as `eg-elb-values.yaml`:

    - Set `controllerName` to `gateway.envoyproxy.io/elb-gatewayclass-controller`.
    - Set the sidecar's `ENVOY_GATEWAY_NAMESPACE` environment variable to `envoy-gateway-elb`.

    Install the controller chart pointed at the new namespace:

    ```bash
    helm install elb-envoy-remote-infra-provider oci://public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider-chart \
      --version $VERSION \
      -n elb-envoy-remote-infra-provider-system --create-namespace \
      --set envoyGatewayNamespace=envoy-gateway-elb
    ```

=== "Webhook"

    Save the `config` section above as `eg-elb-values.yaml`. Install the controller chart first, pointed at the new namespace, so the sidecar is injected when the Envoy Gateway pod starts:

    ```bash
    helm install elb-envoy-remote-infra-provider oci://public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider-chart \
      --version $VERSION \
      -n elb-envoy-remote-infra-provider-system --create-namespace \
      --set webhook.enabled=true \
      --set clusterName=$CLUSTER_NAME \
      --set aws.region=$AWS_REGION \
      --set aws.vpcId=$VPC_ID \
      --set envoyGatewayNamespace=envoy-gateway-elb
    ```

Grant the controller AWS Identity and Access Management (IAM) policy to the `envoy-gateway` service account in `envoy-gateway-elb`, as in [Installation Guide Step 2](installation.md#step-2-grant-the-policy-to-the-envoy-gateway-service-account). Then install Envoy Gateway. The CRDs are already installed by your existing install, so skip them:

```bash
helm install eg-elb oci://docker.io/envoyproxy/gateway-helm --version v1.10.0 \
  -n envoy-gateway-elb --create-namespace --skip-crds \
  -f eg-elb-values.yaml
```

With IAM roles for service accounts (IRSA), annotate the `envoy-gateway` service account in `envoy-gateway-elb` and restart Envoy Gateway, as in [Installation Guide Step 4](installation.md#step-4-install-envoy-gateway).

Check that the new Envoy Gateway pod has the sidecar, as in [Installation Guide Step 5](installation.md#step-5-verify-the-installation), using the `envoy-gateway-elb` namespace.

## Step 2: Create the new GatewayClass

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: elb-proxy
  namespace: envoy-gateway-elb
spec:
  telemetry:
    accessLog:
      disable: true
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: elb-proxy-aws
spec:
  controllerName: gateway.envoyproxy.io/elb-gatewayclass-controller
  parametersRef:
    group: gateway.envoyproxy.io
    kind: EnvoyProxy
    name: elb-proxy
    namespace: envoy-gateway-elb
```

Copy any other settings from your current `EnvoyProxy` that still apply, such as metrics sinks, tracing, and bootstrap customization.

## Step 3: Create new Gateways

For each Gateway you're migrating, create a copy in the **same namespace** with a new name and `gatewayClassName: elb-proxy-aws`. Keep the same listeners. Until you attach an `ELBProxyConfiguration`, the new Gateway shows `Programmed=False` with reason `AddressNotAssigned`, and no proxy is created:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway-elb
  namespace: my-app
spec:
  gatewayClassName: elb-proxy-aws
  listeners:
    # Same listeners as my-gateway
    - name: http
      protocol: HTTP
      port: 80
```

Then attach an `ELBProxyConfiguration`, using the settings you mapped above:

```yaml
apiVersion: gateway.elbproxy.aws/v1alpha1
kind: ELBProxyConfiguration
metadata:
  name: my-gateway-elb
  namespace: my-app
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: my-gateway-elb
  roleArn: arn:aws:iam::123456789012:role/my-proxy-role
  networkConfiguration:
    ipConfiguration:
      ipAddressingConfiguration:
        ipv4Addressability: internet-facing
        ipv6Addressability: "off"
```

!!! tip
    To apply one configuration to every migrated Gateway, target the GatewayClass instead, from an `ELBProxyConfiguration` in `envoy-gateway-elb`.

## Step 4: Attach your routes to both Gateways

Add the new Gateway to each route's `parentRefs`, so the old and new Gateways serve the same routes during the migration:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-route
  namespace: my-app
spec:
  parentRefs:
    - name: my-gateway
    - name: my-gateway-elb
  rules:
    # unchanged
```

Its own Envoy Gateway install handles each Gateway, so the existing Gateway keeps serving traffic unchanged.

## Step 5: Test the new Gateway

Wait for the new Gateway to be programmed, and get its address:

```bash
kubectl wait gateway/my-gateway-elb -n my-app --for=condition=Programmed --timeout=15m
export NEW_HOST=$(kubectl get gateway my-gateway-elb -n my-app \
  -o jsonpath='{.status.addresses[0].value}')
```

Send test requests directly to the new address, with the `Host` header your routes expect:

```bash
curl -i -H "Host: www.example.com" http://$NEW_HOST/
```

Check routing, TLS, headers, and backend behavior the same way you test the current Gateway. If something fails, see [Troubleshooting](../troubleshooting/index.md).

## Step 6: Shift traffic

The new Gateway has a new address, so you move clients to it. How you do that depends on how clients reach your Gateways today.

!!! warning "Check how clients connect before you shift"
    If clients use your own hostname, such as `api.example.com`, you can shift traffic by changing DNS records. If clients call the old address directly, such as the NLB's DNS name, you can't shift traffic without updating every client. Plan for this before you migrate.

The exact steps depend on your DNS provider and traffic management tooling. Look for the ability to shift traffic gradually and to roll back quickly. For example, with Amazon Route 53 weighted records:

1. Create a weighted `CNAME` record for your hostname that points at the old address, and a second one that points at `$NEW_HOST`, with a small weight on the new one. A `CNAME` record can't be used at the zone apex.
2. Watch error rates and latency on both.
3. Increase the weight on the new address until it takes all traffic.

Keep the old Gateway running until the old address gets no traffic.

To roll back, move the DNS weight back to the old address. The old Gateway is unchanged until you delete it.

## Step 7: Clean up

When the old Gateways get no traffic:

1. Remove the old Gateway from each route's `parentRefs`.
2. Delete the old Gateways. If an NLB was in front, deleting the old Envoy deployment removes its Service and the NLB.
3. When no Gateways are left on the old install, uninstall it, and delete its GatewayClass and `EnvoyProxy`.
4. `helm uninstall` leaves some Envoy Gateway resources behind: the `certgen` ClusterRole and ClusterRoleBinding, and the `envoy-gateway-topology-injector` MutatingWebhookConfiguration, each named for the old release or namespace. Delete them.
