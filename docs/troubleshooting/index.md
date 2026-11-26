# Troubleshooting

This page helps you diagnose common problems with the sidecar, the controller, and `ELBProxyConfiguration` resources. Start with the quick checks, then find your symptom in the following sections.

--8<-- "prerequisites.md"

## Quick checks

**1. Is the sidecar running in the Envoy Gateway pod?**

```bash
kubectl get pods -n envoy-gateway-system \
  -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.initContainers[*].name}{"\n"}{end}'
```

Each Envoy Gateway pod should list `elb-envoy-remote-infra-provider`.

**2. What does the sidecar log say?**

```bash
kubectl logs -n envoy-gateway-system deployment/envoy-gateway \
  -c elb-envoy-remote-infra-provider --tail=200
```

Only one sidecar, the leader, calls AWS. If you run several Envoy Gateway replicas, check each pod, or find the leader:

```bash
kubectl get lease elb-envoy-remote-infra-provider-leader \
  -n envoy-gateway-system -o jsonpath='{.spec.holderIdentity}{"\n"}'
```

**3. Is the ELBProxyConfiguration accepted, and are its proxies active?**

```bash
kubectl get elbproxyconfiguration -A \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,ACCEPTED:.status.conditions[?(@.type=="Accepted")].reason,PROGRAMMED:.status.conditions[?(@.type=="Programmed")].message'
```

`PROGRAMMED` shows a message such as `1 of 2 backing proxies are active`. If any proxy isn't active, check its entry in `status.proxies`. See [Check the proxy's status](#check-the-proxys-status).

**4. What do the Gateway events say?**

```bash
kubectl describe gateway <name> -n <namespace>
```

Look for `SuccessfullyReconciled`, `FailedDeployModel`, or `WaitingForProxyDeletion` events from `elbproxy-gateway-extension`.

## Installation issues

### Envoy Gateway pods don't have the sidecar

=== "Envoy Gateway Helm values (default)"

    - Check that Envoy Gateway was installed or upgraded with your filled-in `eg-values.yaml` (`helm get values eg -n envoy-gateway-system`), and that it includes the `deployment.pod.initContainers` sidecar.
    - Check that you're on Envoy Gateway **v1.10** or later. Older charts ignore `deployment.pod.initContainers`.

=== "Webhook"

    The webhook only adds the sidecar when a pod is created.

    - If you installed the chart after Envoy Gateway, restart Envoy Gateway:

        ```bash
        kubectl rollout restart deployment envoy-gateway -n envoy-gateway-system
        ```

    - Check that `envoyGatewayNamespace` matches the namespace Envoy Gateway runs in. The webhook ignores pods in other namespaces.
    - Check that `webhook.enabled=true` is set and the webhook is running:

        ```bash
        kubectl get deployment -n elb-envoy-remote-infra-provider-system
        ```

### Envoy Gateway pods can't be created

Events on the Envoy Gateway ReplicaSet show `failed calling webhook "mpod.gateway.elbproxy.aws"`.

With the default `webhook.failurePolicy: Fail`, Envoy Gateway pods can't be created while the webhook is unavailable. Fix the webhook deployment first. The ReplicaSet retries on its own once it's back.

### The sidecar keeps restarting

Check the sidecar's previous logs:

```bash
kubectl logs -n envoy-gateway-system <envoy-gateway-pod> \
  -c elb-envoy-remote-infra-provider --previous
```

The following table lists common startup errors and their fixes.

| Error | Fix |
|-------|-----|
| `region not resolved; specify --aws-region or set AWS_REGION / AWS_DEFAULT_REGION` | Set the sidecar's `--aws-region`: in `eg-values.yaml` (default), or `aws.region` in the chart values (webhook). |
| `NODENAME "..." is not an instance ID; set --aws-vpc-id or --aws-vpc-tags` | Set the sidecar's `--aws-vpc-id`: in `eg-values.yaml` (default), or `aws.vpcId` in the chart values (webhook). |
| `resolved VPC ID is empty`, `no VPC found with tags`, or `multiple VPCs found with tags` | Set `--aws-vpc-id`, or make the VPC tags match exactly one VPC. |
| `--sidecar-image is required when --enable-sidecar-webhook is set` | The webhook deployment has no sidecar image. The chart sets it from `image.repository` and `image.tag`, so check those values. |
| `tag key "..." cannot be specified in --default-tags (reserved by controller)` | A reserved `gateway.elbproxy.aws/` key is in `--default-tags` or `--external-managed-tags`, or a key is in both flags. Remove it. See [Configurations](../deploy/configurations.md#tagging). |
| `AccessDenied` or `UnauthorizedOperation` | Envoy Gateway's service account doesn't have the controller AWS Identity and Access Management (IAM) policy. See [Installation Guide Step 2](../deploy/installation.md#step-2-grant-the-policy-to-the-envoy-gateway-service-account). With IRSA, check the annotation is still on the service account: `helm uninstall eg` removes it. After fixing, restart Envoy Gateway so the pod gets credentials. |

## The Gateway has no address

Work through these in order.

**No ELBProxyConfiguration attached.** The controller only creates a proxy for a Gateway that has an `ELBProxyConfiguration`. The sidecar logs `ELBProxyConfiguration not resolved, skipping reconcile`. Check that:

- `spec.targetRefs` names the Gateway exactly, with `group: gateway.networking.k8s.io` and `kind: Gateway`.
- The `ELBProxyConfiguration` is in the **same namespace** as the Gateway.
- For a GatewayClass target, the `ELBProxyConfiguration` is in the Envoy Gateway namespace.

**The ELBProxyConfiguration isn't accepted.** See [Accepted is False](#accepted-is-false).

**The deploy failed before a proxy was created.** Look for a `FailedDeployModel` event on the Gateway. See [FailedDeployModel errors](#faileddeploymodel-errors).

**The proxy exists but isn't active.** Check its status. See [Check the proxy's status](#check-the-proxys-status). Envoy Gateway shows this on the Gateway as `Programmed=False` with reason `AddressNotAssigned`.

## Check the proxy's status

Once a proxy exists, its state is in the `ELBProxyConfiguration`'s `status.proxies`, one entry per Gateway, with the proxy Amazon Resource Name (ARN):

```bash
kubectl get elbproxyconfiguration <name> -n <namespace> \
  -o jsonpath='{range .status.proxies[*]}{.gatewayRef.namespace}/{.gatewayRef.name}{"  "}{.proxyArn}{"\n    "}{.conditions[?(@.type=="Programmed")].reason}{": "}{.conditions[?(@.type=="Programmed")].message}{"\n"}{end}'
```

The following table lists each reason and what to do about it.

| Reason | What to do |
|--------|------------|
| `Programmed` | The proxy is active. If requests still fail, see [The proxy is up but requests fail](#the-proxy-is-up-but-requests-fail). |
| `Pending` | The proxy is being created, which takes several minutes. The controller checks again every 2 minutes, and the Gateway gets its address when creation finishes. If it stays `Pending` much longer than usual, open a support case with the proxy ARN. |
| `ActionRequired` | The proxy needs action from you. The message lists each reason from AWS as `<code>: <message> [<target>]`. Fix what it names, such as a resource in `[<target>]`. The condition updates on the next reconcile. |
| `CreateFailed` | The proxy failed to create. Check the sidecar logs around the time it was created, then open a support case with the proxy ARN. |
| `Deleting` | The proxy is being deleted. `Proxy is being deleted` means the Gateway was deleted. `Replacing the proxy: deleting the old proxy` means a change requires a new proxy and the new proxy has the same name (you set `spec.name`). The controller creates the new one after the old one is gone, and the Gateway shows `WaitingForProxyDeletion` meanwhile. Without `spec.name`, the new proxy has a new name, so the controller creates it right away. |

!!! warning "`SuccessfullyReconciled` doesn't mean the proxy is healthy"
    With `ActionRequired` or `CreateFailed`, the controller's own work still succeeded, so the Gateway gets a `SuccessfullyReconciled` event, not a warning. The `ELBProxyConfiguration` status is the only place these states appear.

The top-level `Programmed` condition is `True` as long as **any** proxy is active, so with several Gateways, check each entry.

## FailedDeployModel errors

The event message is `Failed deploy model due to` followed by the error from the failing step. Errors returned by AWS appear in it too, for example `failed to create proxy: ...`. The following table lists common ones.

| Message contains | Cause and fix |
|------------------|---------------|
| `access logging on AWS-managed proxy must either be disabled ...` | The Gateway's `EnvoyProxy` uses Envoy Gateway's default file access log. Set `telemetry.accessLog.disable: true`, or use an OpenTelemetry or Access Log Service (ALS) sink. |
| `unable to resolve at least one subnet` | No eligible subnets found. If you rely on discovery, tag at least two subnets in different Availability Zones with `kubernetes.io/role/elb=1` (internet-facing) or `kubernetes.io/role/internal-elb=1` (internal), or list subnets in `spec.networkConfiguration.vpcConfiguration.subnets`. The message also says how many subnets were skipped for other clusters' tags, fewer than 8 free IP addresses, or (for IPv6 proxies) no IPv6 CIDR block. See [Subnet discovery](../deploy/subnet_discovery.md). |
| `subnets count less than minimal required count` | The proxy needs at least two subnets. |
| `multiple subnets in same Availability Zone` | Pick one subnet per Availability Zone. |
| `do not have an associated IPv6 CIDR block` | IPv6 is enabled, so every subnet needs an IPv6 CIDR. |
| `attribute "..." is read-only and cannot be changed from "..." to "..."` | You changed an attribute that can't change after the proxy is created. Set the original value again. For `waf.web_acl_arn`, change the web ACL with the AWS WAF API, then set the spec to match. See [AWS WAF](../guide/integrations.md#aws-waf). |
| `attribute "..." is managed by the controller` | Remove the Envoy log-level attributes from `spec.attributes`. Set `EnvoyProxy.spec.logging.level` instead. |
| `tag key "..." is externally managed` | That tag key is in `--external-managed-tags`. Remove it from `spec.tags`. |
| `no node security groups discovered` or `expected exactly one SG tagged "kubernetes.io/cluster/<name>"` | The controller couldn't find the security groups of your nodes. Check that the nodes are Ready EC2 instances with an AWS `spec.providerID`. If a node's primary network interface has several security groups, tag exactly one of them `kubernetes.io/cluster/<cluster-name>`. |
| `AccessDenied` | The controller IAM policy is missing an action, or the resource isn't tagged as controller-managed. Check the policy against [`k8s/iam-policy.json`](https://github.com/aws/elb-envoy-remote-infra-provider/blob/v1.0.0/k8s/iam-policy.json). |

The controller retries failed deploys automatically. After you fix the cause, the next retry picks up the change.

## Accepted is False

The following table lists the reasons.

| Reason | Cause and fix |
|--------|---------------|
| `Conflicted` | Another `ELBProxyConfiguration` targets the same Gateway and takes precedence. A Gateway-scoped one takes precedence over a GatewayClass-scoped one, and the oldest takes precedence within the same scope, then the lowest namespace and name. Delete or retarget one of them. |
| `InvalidTargetRef` | It targets a GatewayClass but isn't in the Envoy Gateway namespace. Move it to that namespace. |

## Can't delete an ELBProxyConfiguration

The delete hangs, and the sidecar logs `ELBProxyConfiguration ... is still in use by gateway ...`.

An `ELBProxyConfiguration` has a finalizer, so it can't be deleted while a Gateway uses it. Delete the Gateway first, or attach a different `ELBProxyConfiguration` to it.

## The proxy is up but requests fail

**Can't connect at all.**

- An internal proxy is only reachable from inside the virtual private cloud (VPC). Use `ipv4Addressability: internet-facing` to reach it from outside. Changing it replaces the proxy, which gets a new address.
- Check `spec.ingressCidrs` and `spec.ingressPrefixLists`. They limit who can reach the listener ports.
- A new proxy's DNS name can take a few minutes to resolve.

**Connects, but gets errors or no response.** The proxy might not be getting its configuration, or might not reach your pods.

- **Custom security groups:** if you set `spec.networkConfiguration.vpcConfiguration.securityGroups`, the controller doesn't add any rules. See [Bring your own security groups](../deploy/security_groups.md#bring-your-own-security-groups) for the full list. You must allow inbound traffic from the proxy to the following:
    - Envoy Gateway pods on TCP 18000 (xDS), and on TCP 18002 (Wasm modules) if you use Wasm extensions
    - your backend pods, and any request mirror, `ext_authz`, or `ext_proc` services, on their target ports
- **Pod networking:** pod IPs must be routable in the VPC. The proxy connects to pod IPs directly.
- **Route status:** check that the route is accepted and its backends resolve:

    ```bash
    kubectl get httproute <name> -n <namespace> -o yaml
    ```

## Global rate limiting doesn't work

Global rate limiting isn't supported. See [Compatibility](../compatibility.md#envoy-gateway-features). Envoy Gateway still translates a `BackendTrafficPolicy` with `type: Global`, so the proxy gets a `ratelimit_cluster` pointing at `envoy-ratelimit.<envoy-gateway-namespace>.svc.cluster.local`. That name doesn't resolve outside the cluster, so the cluster has no healthy hosts. What happens next depends on `failClosed` in the `EnvoyGateway` config's `rateLimit` section:

The following table shows what happens for each `failClosed` value.

| `failClosed` | Symptom |
|--------------|---------|
| `false` (default) | Requests pass, and **the limit isn't enforced**. Nothing reports an error. |
| `true` | Every request on the affected routes gets **HTTP 500**. Other routes keep working. |

The controller doesn't report either case. To fix it, delete the `BackendTrafficPolicy`, or change it to `type: Local`. The proxy drops the `ratelimit_cluster` and the 500s stop, with no restart. If you don't use global rate limiting, remove the `rateLimit` section from the `EnvoyGateway` config too.

## Collect information for a bug report

Include:

```bash
kubectl get gateway,httproute,elbproxyconfiguration -A -o yaml > resources.yaml
kubectl describe gateway -A > gateway-events.txt
kubectl logs -n envoy-gateway-system deployment/envoy-gateway \
  -c elb-envoy-remote-infra-provider --tail=1000 > sidecar.log
kubectl logs -n envoy-gateway-system deployment/envoy-gateway \
  -c envoy-gateway --tail=1000 > envoy-gateway.log
```

Also include the controller and Envoy Gateway versions, and the proxy ARN from `status.proxies[].proxyArn` if one was created. Remove account IDs, role ARNs, and other sensitive values before sharing.
