# Controller configuration options

This page covers the command-line flags of the `elb-envoy-remote-infra-provider` binary.

The binary runs in two places:

- **Sidecar**: runs inside the Envoy Gateway pod. It serves the gRPC socket Envoy Gateway calls and manages the AWS resources.
- **Webhook deployment** (optional): installed by the Helm chart when `webhook.enabled=true`. It runs the mutating webhook that injects the sidecar into the Envoy Gateway pod.

Most of these flags configure the sidecar. Where you set them depends on how you add the sidecar. See [Installation Guide](installation.md#choose-how-to-add-the-sidecar).

## Setting sidecar flags

=== "Envoy Gateway Helm values (default)"

    Set flags directly in the sidecar's `args` in your Envoy Gateway values (based on `k8s/envoy-gateway-values.yaml`):

    ```yaml
    deployment:
      pod:
        initContainers:
          - name: elb-envoy-remote-infra-provider
            args:
              - --server-socket-path=/var/run/elb-envoy-remote-infra-provider/server.sock
              - --cluster-name=my-cluster
              - --aws-region=us-west-2
              - --aws-vpc-id=vpc-0123456789abcdef0
              - --default-tags=team=platform,env=prod
    ```

=== "Webhook"

    The chart turns `clusterName`, `aws.region`, and `aws.vpcId` into sidecar flags. Add any other sidecar flag to `webhook.sidecar.extraArgs`, one flag per entry:

    ```yaml
    clusterName: my-cluster         # --cluster-name (required)
    aws:
      region: us-west-2             # --aws-region
      vpcId: vpc-0123456789abcdef0  # --aws-vpc-id
    webhook:
      enabled: true
      sidecar:
        extraArgs:
          - --default-tags=team=platform,env=prod
          - --aws-api-endpoints=EC2=https://ec2.example.com
    ```

    The webhook passes each entry to the sidecar unchanged, so values that contain commas work. Don't repeat `clusterName`, `aws.region`, or `aws.vpcId` there.

    The chart's top-level `extraArgs` and `env` apply to the **webhook deployment only**. They don't reach the sidecar.

## Controller command-line flags

### General

The following table lists the general flags.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `cluster-name` | string | `CLUSTER_NAME` environment variable | Kubernetes cluster name. Used to tag the AWS resources the controller creates. |
| `log-level` | string | `info` | Log level: `debug`, `info`, or `error`. |
| `metrics-bind-address` | string | `:8082` | Address the metrics server binds to. Defaults to `:8082` to avoid Envoy Gateway's `:8080`. |
| `leader-election-namespace` | string | `POD_NAMESPACE` environment variable | Namespace for the leader election lease. |
| `sync-period` | duration | `10h0m0s` | Period at which the controller forces a resync of its local object stores. |
| `server-socket-path` | string | `/var/run/elb-envoy-remote-infra-provider/server.sock` | Path of the gRPC Unix domain socket the server listens on. |
| `server-socket-mode` | uint32 | `0660` | File mode (octal) applied to the socket file. |

### AWS

The following table lists the flags that configure AWS access.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `aws-region` | string | `AWS_REGION` / `AWS_DEFAULT_REGION` environment variable | AWS Region of the cluster. The controller fails to start if no region is resolved. |
| `aws-vpc-id` | string | auto-detected | VPC for the proxy resources. See [VPC detection](#vpc-detection). |
| `aws-vpc-tags` | stringMap | | Find the VPC by tags instead of ID, for example `env=prod,team=platform`. Exactly one VPC must match. |
| `aws-max-attempts` | int | `10` | Total attempts (initial request plus retries) for AWS API calls. `0` uses the SDK default of 3. |
| `aws-api-endpoints` | stringMap | | Custom endpoint overrides, format `serviceID1=URL1,serviceID2=URL2`. Supported service IDs: `EC2` and `ELB Proxy`. Useful for VPC endpoints or local testing. |

### Tagging

The following table lists the flags that control AWS tags.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `default-tags` | stringMap | | AWS tags applied to every proxy, format `key1=value1,key2=value2`. Tags set on `ELBProxyConfiguration` take precedence. |
| `external-managed-tags` | stringList | | AWS tag keys on proxies that are managed outside the controller. The controller never adds, updates, or removes these keys on proxies. This doesn't apply to security groups: the controller removes any tag it didn't set from the security groups it creates. |

The controller reserves these tag keys. They can't be used in `default-tags` or `external-managed-tags`:

- `gateway.elbproxy.aws/cluster`
- `gateway.elbproxy.aws/stack`
- `gateway.elbproxy.aws/resource`
- `gateway.elbproxy.aws/managed-by`

A key also can't appear in both `default-tags` and `external-managed-tags`.

Don't use any key that starts with `gateway.elbproxy.aws/` in `spec.tags` or `--default-tags`. The controller rejects the four keys above only in the two flags, not in `spec.tags`, and it doesn't block the other keys with this prefix. A tag you set replaces the tag the controller uses to find its resources.

### Sidecar webhook

These flags apply only to the webhook deployment. The Helm chart sets them when `webhook.enabled=true`.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `enable-sidecar-webhook` | boolean | `false` | Run only the mutating webhook that injects the sidecar into the Envoy Gateway pod. |
| `webhook-bind-port` | int | `9443` | Port the webhook server listens on. |
| `webhook-cert-dir` | string | `/tmp/k8s-webhook-server/serving-certs` | Directory holding the webhook's `tls.crt` and `tls.key`. |
| `health-probe-bind-addr` | string | `:61779` | Address the webhook serves `/readyz` on. Ready once the webhook server completes a TLS handshake. |
| `sidecar-image` | string | | Image injected as the sidecar. Required when `enable-sidecar-webhook` is set. |
| `sidecar-image-pull-policy` | string | `IfNotPresent` | Pull policy for the sidecar image: `Always`, `IfNotPresent`, or `Never`. |
| `sidecar-mount-certs` | boolean | `true` | Mount the Envoy Gateway certificates volume into the sidecar. |
| `sidecar-extra-args` | stringArray | | One flag to pass to the injected sidecar. Repeat for each flag. The chart sets this from `clusterName`, `aws.region`, `aws.vpcId`, and `webhook.sidecar.extraArgs`. |

## VPC detection

When `aws-vpc-id` isn't set, the controller resolves the virtual private cloud (VPC) in this order:

1. If `aws-vpc-tags` is set, it looks up the single VPC matching all the tags.
2. Otherwise, it reads an EC2 instance ID from the `NODENAME` environment variable and looks up that instance's VPC.

!!! tip
    Set `aws-vpc-id` explicitly. Neither install option sets `NODENAME`, so the second step fails unless you set it yourself. If the controller can't resolve the VPC, it fails to start with the error `NODENAME "" is not an instance ID`.

## What the injected sidecar receives

With the webhook, the injected sidecar gets:

- `--server-socket-path`, set by the webhook to match the shared socket volume.
- Every `--sidecar-extra-args` value, as its own flag, in order. This includes the flags the chart builds from `clusterName`, `aws.region`, and `aws.vpcId`.
- The `ENVOY_GATEWAY_NAMESPACE` and `POD_NAMESPACE` environment variables.

The webhook doesn't forward any of its own other flags. When `aws.region` is empty, the sidecar falls back to the `AWS_REGION` environment variable, which IAM roles for service accounts (IRSA) set. Set `aws.region` to avoid depending on it.

!!! warning
    Don't override `--server-socket-path` in `webhook.sidecar.extraArgs`. It must match Envoy Gateway's socket path and the sidecar's mounts and probes.
