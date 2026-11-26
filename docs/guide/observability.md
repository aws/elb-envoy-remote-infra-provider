# Observability

The proxy runs in your virtual private cloud (VPC), outside the cluster. Its logs and metrics don't appear in the cluster on their own: the proxy has to send them over the network to a collector you run, or your scraper has to reach the proxy. This page covers both, and the network setup each needs.

You configure telemetry as you normally do in Envoy Gateway, in the `EnvoyProxy` that your GatewayClass references, for example `elb-proxy` from [Getting Started](../examples/getting_started.md).

The following table lists the telemetry signals and how to configure each one.

| Signal | How | Notes |
|--------|-----|-------|
| Access logs | OpenTelemetry or Access Log Service (ALS) sink in `telemetry.accessLog` | File sinks, including Envoy Gateway's default `/dev/stdout`, don't work. See [Access logs](#access-logs). |
| Metrics, pushed | OpenTelemetry sink in `telemetry.metrics.sinks` | Recommended for metrics. See [Push metrics](#push-metrics-with-opentelemetry). |
| Metrics, pulled | Prometheus endpoint on the proxy, port `19001` | Needs your own security groups. See [Pull metrics](#pull-metrics-with-prometheus). |

## How the proxy reaches your collector

The proxy connects to a collector in your VPC the same way it connects to your backend pods: directly, by IP. The source of that traffic depends on how the proxy's security groups are set up.

**Controller-managed security groups (default).** Every proxy the controller creates in the cluster has one shared **backend security group**. That's the source of the proxy's traffic to your pods. The controller adds rules on your node security group that allow it in, but only for Envoy Gateway's xDS port (`18000`) and its Wasm module port (`18002`), which it opens while any Gateway is active, and the ports your Gateways send traffic to, such as your backends and `ext_authz` or `ext_proc` services. It never opens a telemetry port, so you add that rule yourself.

Find the backend security group:

```bash
export BACKEND_SG=$(aws ec2 describe-security-groups \
  --filters Name=tag:gateway.elbproxy.aws/cluster,Values=$CLUSTER_NAME \
            Name=tag:gateway.elbproxy.aws/resource,Values=BackendSecurityGroup \
  --query 'SecurityGroups[].GroupId' --output text)
```

**Your own security groups.** If you set `spec.networkConfiguration.vpcConfiguration.securityGroups`, there's no backend security group and the controller doesn't manage any rules. Use your security groups as the source instead.

See [Security groups](../deploy/security_groups.md) for how both modes work.

!!! warning
    If this rule is missing, the proxy can't connect to the collector, and nothing in the cluster reports it. Envoy Gateway and the Gateway both look healthy, and no data arrives. A connection that is already open keeps working, so the problem can appear later, for example after the collector restarts.

## Access logs

Access logs are sent with an OpenTelemetry sink to a collector in the cluster, which forwards them to wherever you store logs, such as CloudWatch Logs or Loki.

```
proxy (Envoy) --OTLP/gRPC :4317--> collector pods --> CloudWatch Logs, Loki, ...
```

### 1. Run a collector

Run an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) in the cluster with an OTLP gRPC receiver on port `4317`, behind a Service. Make sure the Service has ready endpoints before you point the sink at it.

To write to CloudWatch Logs, use the collector's [`awscloudwatchlogs` exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/awscloudwatchlogsexporter), and give the collector's service account an AWS Identity and Access Management (IAM) role that can write to your log group.

### 2. Allow the proxy to reach the collector pods

Allow TCP `4317` into your node security group (or the collector pods' security group) from the [backend security group](#how-the-proxy-reaches-your-collector). For example, with the EKS cluster security group:

```bash
NODE_SG=$(aws eks describe-cluster --name $CLUSTER_NAME \
  --query 'cluster.resourcesVpcConfig.clusterSecurityGroupId' --output text)

aws ec2 authorize-security-group-ingress --group-id $NODE_SG \
  --ip-permissions "IpProtocol=tcp,FromPort=4317,ToPort=4317,UserIdGroupPairs=[{GroupId=$BACKEND_SG,Description=elb-proxy-otlp}]"
```

### 3. Configure the sink

Point the sink at the collector Service with `backendRefs`. Envoy Gateway resolves it to the collector's pod IPs, which the proxy can reach directly in the VPC:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: elb-proxy
  namespace: envoy-gateway-system
spec:
  telemetry:
    accessLog:
      settings:
        - type: Route
          format:
            type: Text
            text: "[%START_TIME%] \"%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)% %PROTOCOL%\" %RESPONSE_CODE% %RESPONSE_FLAGS% %BYTES_RECEIVED% %BYTES_SENT% %DURATION% %RESPONSE_DURATION% \"%REQ(X-FORWARDED-FOR)%\" \"%REQ(USER-AGENT)%\" \"%REQ(X-REQUEST-ID)%\" \"%REQ(:AUTHORITY)%\" \"%UPSTREAM_HOST%\"\n"
          sinks:
            - type: OpenTelemetry
              openTelemetry:
                backendRefs:
                  - kind: Service
                    name: otel-collector
                    namespace: observability
                    port: 4317
                resources:
                  service.name: elb-proxy
```

Envoy Gateway sends access-log changes to the proxy over xDS. The proxy isn't replaced.

### Text or JSON

- **`format.type: Text`** puts the formatted line in the log record's body. To write the line to CloudWatch as-is, set `raw_log: true` on the `awscloudwatchlogs` exporter.
- **`format.type: JSON`** puts each field in the log record's **attributes** and leaves the body empty. Turn `raw_log` off, or CloudWatch receives empty lines.

```yaml
          format:
            type: JSON
            json:
              start_time: "%START_TIME%"
              method: "%REQ(:METHOD)%"
              path: "%REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%"
              response_code: "%RESPONSE_CODE%"
              response_flags: "%RESPONSE_FLAGS%"
              duration: "%DURATION%"
              upstream_time: "%RESPONSE_DURATION%"
              client_ip: "%DOWNSTREAM_REMOTE_ADDRESS_WITHOUT_PORT%"
              host: "%REQ(:AUTHORITY)%"
              request_id: "%REQ(X-REQUEST-ID)%"
              upstream_host: "%UPSTREAM_HOST%"
```

!!! note "Upstream latency"
    Envoy's default text format includes `%RESP(X-ENVOY-UPSTREAM-SERVICE-TIME)%`. The proxy removes that header, so the field is always `-`. Use `%RESPONSE_DURATION%` for upstream latency, as in the examples above.

### File sinks

The managed proxy doesn't support the File access logger, so a File sink, such as Envoy Gateway's default `/dev/stdout`, doesn't work. The controller rejects it: the Gateway gets a `FailedDeployModel` Warning event, and the controller creates no new proxy.

The controller can't stop Envoy Gateway from sending the listener to a proxy that **already exists**, though. If you switch a running proxy's `EnvoyProxy` to a File sink, the Gateway gets the same `FailedDeployModel` event. Envoy rejects the update because it has no `envoy.access_loggers.file` implementation, and keeps serving the last good listener. Traffic keeps working, logs keep arriving in the old format, and route changes still apply, but listener changes don't, until you fix the `EnvoyProxy`. The rejection only shows in Envoy Gateway's logs:

```bash
kubectl logs -n envoy-gateway-system deployment/envoy-gateway -c envoy-gateway --tail=100 \
  | grep -i rejected
```

Reapply a working sink to recover. No restart is needed.

## Push metrics with OpenTelemetry

The proxy can export metrics with Envoy's OpenTelemetry stats sink. The controller writes the sink into the proxy's bootstrap. For metrics, point the sink at a stable address, such as an internal NLB in front of the collector: with `backendRefs`, the controller writes only the first resolved endpoint into the bootstrap, so metrics go to one collector pod.

### Install a collector

Envoy Gateway's [add-ons Helm chart](https://gateway.envoyproxy.io/docs/install/gateway-addons-helm-api/) installs Prometheus, Grafana, and an OpenTelemetry Collector. Expose the collector through an internal NLB. This uses the [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/), which must be installed in the cluster:

```bash
helm install eg-addons oci://docker.io/envoyproxy/gateway-addons-helm --version v1.10.0 \
  -n monitoring --create-namespace \
  --set opentelemetry-collector.enabled=true \
  --set opentelemetry-collector.service.type=LoadBalancer \
  --set opentelemetry-collector.service.annotations."service\.beta\.kubernetes\.io/aws-load-balancer-type"=external \
  --set opentelemetry-collector.service.annotations."service\.beta\.kubernetes\.io/aws-load-balancer-nlb-target-type"=ip \
  --set opentelemetry-collector.service.annotations."service\.beta\.kubernetes\.io/aws-load-balancer-scheme"=internal \
  --set opentelemetry-collector.config.exporters.prometheus.endpoint=0.0.0.0:19001 \
  --set-string 'opentelemetry-collector.podAnnotations.prometheus\.io/scrape=true' \
  --set-string 'opentelemetry-collector.podAnnotations.prometheus\.io/port=19001'
```

- `aws-load-balancer-type: external` hands the Service to the AWS Load Balancer Controller. It doesn't make the NLB public: `aws-load-balancer-scheme` does, and `internal` is enough because the proxy is in the same VPC. Set these annotations when you create the Service; the AWS Load Balancer Controller documents that changing `aws-load-balancer-type` on an existing Service can leak resources.
- The `prometheus.io` annotations let the bundled Prometheus scrape the collector.
- For a proxy with many routes, raise the collector's gRPC message limit from the 4 MB default:

    ```bash
    --set opentelemetry-collector.config.receivers.otlp.protocols.grpc.max_recv_msg_size_mib=16
    ```

Get the collector's NLB hostname:

```bash
kubectl get svc -n monitoring -l app.kubernetes.io/name=opentelemetry-collector \
  -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}'
```

### Allow the proxy to reach the collector

With the Service from the previous step, you don't need to add a rule. The AWS Load Balancer Controller creates a security group for the NLB that allows the Service's ports, including `4317`, from `0.0.0.0/0`. If you restrict the sources, for example with `loadBalancerSourceRanges`, or you use your own security group on the NLB, allow inbound TCP `4317` on it from the [backend security group](#how-the-proxy-reaches-your-collector), or from your own security groups if you set them.

### Enable the sink

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: elb-proxy
  namespace: envoy-gateway-system
spec:
  telemetry:
    accessLog:
      disable: true
    metrics:
      sinks:
        - type: OpenTelemetry
          openTelemetry:
            host: <collector-nlb-hostname>
            port: 4317
```

Set `host` to an address the proxy can reach from the VPC. Cluster DNS names such as `otel-collector.monitoring.svc` don't resolve on the proxy.

### Grafana dashboards

The add-ons chart includes Envoy dashboards written for Envoy's Prometheus endpoint. Metrics sent through the OpenTelemetry sink are named differently, so the dashboards need changes:

- There's no `envoy_` prefix. For example, `envoy_server_live` arrives as `server_live`.
- The collector's Prometheus exporter adds `_total` to counters that don't already end in it, for example `cluster_upstream_rq_xx` becomes `cluster_upstream_rq_xx_total`.

## Pull metrics with Prometheus

The proxy serves Envoy's Prometheus stats on port `19001` at `/stats/prometheus`. It's on by default. To turn it off, set `telemetry.metrics.prometheus.disable: true` in the `EnvoyProxy`.

Reaching the port needs your own security groups:

!!! warning "Managed security groups don't open 19001"
    The frontend security group the controller manages only allows the Gateway's listener ports, and on every reconcile it removes rules it didn't create. A rule you add manually for `19001` is removed again. To scrape the proxy, set `spec.networkConfiguration.vpcConfiguration.securityGroups` and allow `19001` from your scraper there. With your own security groups, you manage all inbound rules, including the listener ports. See [Security groups](overview.md#security-groups).

The proxy's IP addresses can change at any time, for example as it scales. Keep your scrape targets current, for example by resolving the Gateway's address or by looking up the proxy's network interfaces with the EC2 `DescribeNetworkInterfaces` API.

Because of these two requirements, the OpenTelemetry sink is usually simpler.

## Troubleshooting

### No access logs arrive, and nothing reports an error

1. Check the node security group allows TCP `4317` from the backend security group. See [step 2](#2-allow-the-proxy-to-reach-the-collector-pods).
2. Check that Envoy accepted the listener: look for `rejected` in Envoy Gateway's logs, as in [File sinks](#file-sinks).
3. Check that the collector receives anything at all, for example with the collector's debug exporter.

### The Gateway shows `Accepted=False` with `InvalidParameters` after adding a sink

The sink's `backendRefs` Service has no ready endpoints. Check the collector pods are running and the Service selects them:

```bash
kubectl get endpoints <collector-service> -n <collector-namespace>
```

### The metrics sink times out

If the proxy's `otel_metric_sink_0` cluster shows a high `upstream_cx_connect_timeout`:

1. Check that the collector NLB exists and has healthy targets:

    ```bash
    kubectl get svc -n monitoring -l app.kubernetes.io/name=opentelemetry-collector
    ```

2. Check that the collector NLB's security group allows TCP `4317` from the proxy. By default it allows it from `0.0.0.0/0`. If you restricted the sources, allow the backend security group.
3. Check that `host` in the `EnvoyProxy` sink is the NLB hostname, not a cluster DNS name.

### No metrics in Prometheus

1. Check that the collector pod is running and has the `prometheus.io/scrape` annotation:

    ```bash
    kubectl get pods -n monitoring -l app.kubernetes.io/name=opentelemetry-collector \
      -o jsonpath='{.items[0].metadata.annotations}'
    ```

2. Check that the collector's Prometheus exporter has data:

    ```bash
    POD_IP=$(kubectl get pod -n monitoring -l app.kubernetes.io/name=opentelemetry-collector \
      -o jsonpath='{.items[0].status.podIP}')
    kubectl run check --rm -i --restart=Never --image=curlimages/curl -- \
      curl -s http://${POD_IP}:19001/metrics | grep -c "cluster_"
    ```

### `received message larger than max`

The proxy is sending more metrics than the collector's gRPC limit allows. Raise it:

```bash
helm upgrade eg-addons oci://docker.io/envoyproxy/gateway-addons-helm --version v1.10.0 \
  -n monitoring --reuse-values \
  --set opentelemetry-collector.config.receivers.otlp.protocols.grpc.max_recv_msg_size_mib=32
```
