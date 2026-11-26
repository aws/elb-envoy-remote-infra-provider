# Merged Gateways

This walkthrough runs two Gateways, owned by two teams, on **one** AWS managed proxy. With Envoy Gateway's merged gateways mode, all Gateways in a GatewayClass share a proxy and its address. The proxy tells the Gateways apart by their listener hostnames.

You create:

1. An `EnvoyProxy` with `mergeGateways: true`, and a `GatewayClass` that uses it.
2. An `ELBProxyConfiguration` for the whole class.
3. For each team, a namespace with an echo backend, a `Gateway`, and an `HTTPRoute`.

For the concepts, see [Merged gateways](../guide/overview.md#merged-gateways).

## Prerequisites

!!! note "Costs"
    This walkthrough creates AWS resources that can incur charges: one ELB for Envoy proxy, plus the network interfaces and security groups that the controller creates in your virtual private cloud (VPC). Delete them when you finish. See [Clean up](#clean-up).

Complete the prerequisites in [Getting Started](getting_started.md#prerequisites): the controller and Envoy Gateway installed, subnets, and the proxy role.

```bash
export PROXY_ROLE_ARN=arn:aws:iam::123456789012:role/my-proxy-role
```

## Step 1: Create the EnvoyProxy and GatewayClass

Set `mergeGateways: true` on the `EnvoyProxy`. A merged class needs its own `EnvoyProxy`, so don't reuse the one from Getting Started.

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: elb-proxy-merged
  namespace: envoy-gateway-system
spec:
  mergeGateways: true
  ipFamily: IPv4
  telemetry:
    accessLog:
      disable: true
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: elb-proxy-aws-merged
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
  parametersRef:
    group: gateway.envoyproxy.io
    kind: EnvoyProxy
    name: elb-proxy-merged
    namespace: envoy-gateway-system
```

Save this as `merged-gatewayclass.yaml` and apply it:

```bash
kubectl apply -f merged-gatewayclass.yaml
```

## Step 2: Attach an ELBProxyConfiguration to the class

In merged mode, only a configuration that targets the GatewayClass applies. Create it in the **Envoy Gateway namespace**, not in a team's namespace:

```yaml
apiVersion: gateway.elbproxy.aws/v1alpha1
kind: ELBProxyConfiguration
metadata:
  name: merged-proxy
  namespace: envoy-gateway-system
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: GatewayClass
      name: elb-proxy-aws-merged
  roleArn: ${PROXY_ROLE_ARN}
  networkConfiguration:
    ipConfiguration:
      ipAddressingConfiguration:
        ipv4Addressability: internet-facing
        ipv6Addressability: "off"
```

Save this as `merged-proxy.yaml` and apply it, substituting the role Amazon Resource Name (ARN):

```bash
envsubst < merged-proxy.yaml | kubectl apply -f -
```

## Step 3: Deploy each team's backend, Gateway, and route

Each team gets a namespace, an echo backend, a Gateway with its own hostname, and a route. The two Gateways use the same port. The hostnames keep them apart.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: team-a
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: team-a
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
  namespace: team-a
spec:
  selector:
    app: echo
  ports:
    - name: http
      port: 8080
      targetPort: 3000
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: team-a
  namespace: team-a
spec:
  gatewayClassName: elb-proxy-aws-merged
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      hostname: a.example.com
      allowedRoutes:
        namespaces:
          from: Same
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: echo
  namespace: team-a
spec:
  parentRefs:
    - name: team-a
  rules:
    - backendRefs:
        - name: echo
          port: 8080
```

Save this as `team-a.yaml`. Make a copy for the second team, replacing `team-a` with `team-b` and `a.example.com` with `b.example.com`, and save it as `team-b.yaml`:

```bash
sed -e 's/team-a/team-b/g' -e 's/a\.example\.com/b.example.com/g' team-a.yaml > team-b.yaml
kubectl apply -f team-a.yaml -f team-b.yaml
```

## Step 4: Wait for the proxy

Provisioning the AWS proxy takes several minutes. Wait for both Gateways to become `Programmed`:

```bash
kubectl wait gateway/team-a -n team-a --for=condition=Programmed --timeout=15m
kubectl wait gateway/team-b -n team-b --for=condition=Programmed --timeout=15m
```

There is one proxy for the class, so one entry in the configuration's status:

```bash
kubectl get elbproxyconfiguration merged-proxy -n envoy-gateway-system \
  -o jsonpath='{range .status.proxies[*]}{.gatewayRef.name}{": "}{.conditions[?(@.type=="Programmed")].message}{"\n"}{end}'
```

Both Gateways report the same address, which is the proxy's DNS name:

```bash
kubectl get gateway -A \
  -o 'custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,ADDRESS:.status.addresses[0].value'
```

## Step 5: Send requests

Send a request to the shared address with each team's hostname. The `Host` header chooses the Gateway, and each reply names the team's namespace:

```bash
export GATEWAY_HOST=$(kubectl get gateway team-a -n team-a \
  -o jsonpath='{.status.addresses[0].value}')
curl -s -H "Host: a.example.com" http://$GATEWAY_HOST/ | grep '"namespace"'
curl -s -H "Host: b.example.com" http://$GATEWAY_HOST/ | grep '"namespace"'
```

The first command prints `"namespace": "team-a"` and the second prints `"namespace": "team-b"`. The DNS name can take a few minutes to resolve after the proxy is created.

## Next steps

- [ELBProxyConfiguration](../guide/overview.md): every setting for the proxy, including which configuration applies in merged mode.
- [Security groups](../deploy/security_groups.md): how the controller opens the listener ports of all the Gateways in the class.

## Clean up

Delete the Gateways first, so the controller removes the AWS proxy and the security groups it created. Then delete the rest:

```bash
kubectl delete -f team-a.yaml -f team-b.yaml
kubectl delete -f merged-proxy.yaml
kubectl delete -f merged-gatewayclass.yaml
```

The controller removes the AWS proxy and the security groups in the background, which takes a few minutes. Keep the controller running until it finishes.
