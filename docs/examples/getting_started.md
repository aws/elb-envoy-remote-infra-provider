# Getting Started

This walkthrough deploys an echo application and exposes it through a Gateway that runs on an AWS managed Envoy proxy.

You create:

1. An `EnvoyProxy` and a `GatewayClass` that Envoy Gateway uses for this walkthrough.
2. An echo backend.
3. A `Gateway` and an `HTTPRoute`.
4. An `ELBProxyConfiguration` that tells the controller to create an AWS proxy for the Gateway.

## Prerequisites

!!! note "Costs"
    This walkthrough creates AWS resources that can incur charges: an ELB for Envoy proxy, plus the network interfaces and security groups that the controller creates in your virtual private cloud (VPC). Follow [Clean up](#clean-up) when you finish to avoid ongoing charges.

- The controller and Envoy Gateway are installed. See the [Installation Guide](../deploy/installation.md).
- At least two public subnets in different Availability Zones. For an internet-facing proxy, the controller looks for subnets tagged `kubernetes.io/role/elb` (value `1` or empty). If no subnet has the tag, it uses subnets with a route to an internet gateway. See [Subnet discovery](../deploy/subnet_discovery.md).
- An AWS Identity and Access Management (IAM) role for the proxy, set as `spec.roleArn`. If you don't have one, create it in [Create the proxy role](#create-the-proxy-role).

## Create the proxy role

Creating a proxy requires a proxy role, which you set as `spec.roleArn`. The proxy uses this role to publish its logs to Amazon CloudWatch Logs, and for the permissions that code in your Envoy configuration, such as filters and extensions, needs when it calls AWS services. To let ELB for Envoy publish the proxy's logs, the role needs at least the following permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ],
      "Resource": [
        "arn:*:logs:*:*:log-group:/aws/elb-proxy/*",
        "arn:*:logs:*:*:log-group:/aws/elb-proxy/*:log-stream:*"
      ]
    }
  ]
}
```

The trust policy of the role must allow `elasticloadbalancing.amazonaws.com` to assume it, so that ELB for Envoy can give the role's credentials to your proxy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Service": "elasticloadbalancing.amazonaws.com"
      },
      "Action": "sts:AssumeRole"
    }
  ]
}
```

To create the role with the AWS CLI, save the two policies as `trust-policy.json` and `logs-policy.json`, then run:

```bash
aws iam create-role --role-name my-proxy-role \
  --assume-role-policy-document file://trust-policy.json

aws iam put-role-policy --role-name my-proxy-role \
  --policy-name ELBProxyEnvoyLogs \
  --policy-document file://logs-policy.json

export PROXY_ROLE_ARN=$(aws iam get-role --role-name my-proxy-role \
  --query Role.Arn --output text)
```

If you already have a role, set `PROXY_ROLE_ARN` to its ARN instead:

```bash
export PROXY_ROLE_ARN=arn:aws:iam::123456789012:role/my-proxy-role
```

## Step 1: Create the EnvoyProxy and GatewayClass

The `GatewayClass` belongs to Envoy Gateway, not to this controller. It points at an `EnvoyProxy` that disables access logging.

!!! warning "Disable access logging or use a network sink"
    Envoy Gateway's default access log writes to `/dev/stdout`, which doesn't exist on the managed proxy. The controller rejects a Gateway whose `EnvoyProxy` uses a file sink or leaves access logging unset. Set `telemetry.accessLog.disable: true`, or use an OpenTelemetry or ALS sink.

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: elb-proxy
  namespace: envoy-gateway-system
spec:
  ipFamily: IPv4
  telemetry:
    accessLog:
      disable: true
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: elb-proxy-aws
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
  parametersRef:
    group: gateway.envoyproxy.io
    kind: EnvoyProxy
    name: elb-proxy
    namespace: envoy-gateway-system
```

Save this as `gatewayclass.yaml` and apply it:

```bash
kubectl apply -f gatewayclass.yaml
```

## Step 2: Deploy the echo backend

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: echo
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: echo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: echo
  template:
    metadata:
      labels:
        app: echo
    spec:
      containers:
        - name: echo
          image: registry.k8s.io/gateway-api/echo-basic:v1.5.1
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          ports:
            - containerPort: 3000
---
apiVersion: v1
kind: Service
metadata:
  name: echo
  namespace: echo
spec:
  selector:
    app: echo
  ports:
    - name: http
      port: 8080
      targetPort: 3000
```

Save this as `echo.yaml` and apply it:

```bash
kubectl apply -f echo.yaml
```

## Step 3: Create the Gateway and HTTPRoute

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: echo-gateway
  namespace: echo
spec:
  gatewayClassName: elb-proxy-aws
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      allowedRoutes:
        namespaces:
          from: Same
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: echo
  namespace: echo
spec:
  parentRefs:
    - name: echo-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: echo
          port: 8080
```

Save this as `gateway.yaml` and apply it:

```bash
kubectl apply -f gateway.yaml
```

The Gateway doesn't have an address yet. The controller only creates an AWS proxy for a Gateway that has an `ELBProxyConfiguration` attached.

## Step 4: Attach an ELBProxyConfiguration

Create the `ELBProxyConfiguration` in the same namespace as the Gateway:

```yaml
apiVersion: gateway.elbproxy.aws/v1alpha1
kind: ELBProxyConfiguration
metadata:
  name: echo-proxy
  namespace: echo
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: echo-gateway
  roleArn: ${PROXY_ROLE_ARN}
  networkConfiguration:
    ipConfiguration:
      ipAddressingConfiguration:
        ipv4Addressability: internet-facing
        ipv6Addressability: "off"
```

Save this as `proxy.yaml` and apply it, substituting the role Amazon Resource Name (ARN):

```bash
envsubst < proxy.yaml | kubectl apply -f -
```

This example sets `internet-facing` so you can reach the proxy from your machine. If you leave `ipConfiguration` unset, the proxy is `internal` and only reachable from inside the VPC.

If you don't set subnets, the controller discovers them from the `kubernetes.io/role/elb` tag. To pick subnets yourself, set `spec.networkConfiguration.vpcConfiguration.subnets` with at least two subnets in different Availability Zones.

By default, the controller creates a security group that allows traffic from `0.0.0.0/0` (plus `::/0` with IPv6) on the listener ports. To restrict it, set `spec.ingressCidrs`.

## Step 5: Wait for the proxy

Check that the `ELBProxyConfiguration` is accepted:

```bash
kubectl get elbproxyconfiguration echo-proxy -n echo \
  -o jsonpath='{.status.conditions[?(@.type=="Accepted")].status}{"\n"}'
```

The output should be `True`.

Provisioning the AWS proxy takes several minutes. The controller checks the proxy's status about every 2 minutes, so the Gateway can become `Programmed` a little after the proxy is active. Wait for the Gateway to become `Programmed`:

```bash
kubectl wait gateway/echo-gateway -n echo \
  --for=condition=Programmed --timeout=15m
```

Watch the Gateway's events for errors. The controller emits no event while the proxy is being created:

```bash
kubectl describe gateway echo-gateway -n echo
```

`FailedDeployModel` means the controller couldn't create or update the proxy. The event message has the error. `SuccessfullyReconciled` means the controller applied the configuration; it doesn't mean the proxy is active.

To see the proxy's own state, check the `ELBProxyConfiguration`. Its `Programmed` condition shows `Pending` while the proxy is being created:

```bash
kubectl get elbproxyconfiguration echo-proxy -n echo \
  -o jsonpath='{.status.proxies[0].conditions[?(@.type=="Programmed")].reason}{": "}{.status.proxies[0].conditions[?(@.type=="Programmed")].message}{"\n"}'
```

If it shows `ActionRequired` or `CreateFailed`, see [Check the proxy's status](../troubleshooting/index.md#check-the-proxys-status).

## Step 6: Send a request

The Gateway's address is the proxy's DNS name:

```bash
export GATEWAY_HOST=$(kubectl get gateway echo-gateway -n echo \
  -o jsonpath='{.status.addresses[0].value}')

curl -i http://$GATEWAY_HOST/
```

The echo server replies with JSON that includes the pod name and namespace. The DNS name can take a few minutes to resolve after the proxy is created.

## Next steps

- [TLS certificates](../guide/tls.md): serve HTTPS with cert-manager or ACM.
- [Observability](../guide/observability.md): send the proxy's metrics to your telemetry stack.

## Clean up

Delete the Gateway first, so the controller removes the AWS proxy and the security groups it created. Then delete the rest:

```bash
kubectl delete -f gateway.yaml
kubectl delete -f proxy.yaml
kubectl delete -f echo.yaml
kubectl delete -f gatewayclass.yaml
```

The controller removes the AWS proxy and the security groups in the background, which takes a few minutes. Keep the controller running until it finishes.

When the proxy is gone, delete the proxy role if you created it in [Create the proxy role](#create-the-proxy-role):

```bash
aws iam delete-role-policy --role-name my-proxy-role --policy-name ELBProxyEnvoyLogs
aws iam delete-role --role-name my-proxy-role
```
