??? note "Prerequisites: install the controller and deploy the example"

    1. Install the controller and Envoy Gateway by following the [Installation Guide](../deploy/installation.md).

    2. Deploy the example Gateway from [Getting Started](../examples/getting_started.md), or set it up in one go:

        ```bash
        export PROXY_ROLE_ARN=arn:aws:iam::123456789012:role/my-proxy-role

        cat <<EOF | kubectl apply -f -
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
        ---
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
        ---
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
            - backendRefs:
                - name: echo
                  port: 8080
        ---
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
        EOF
        ```

    3. Wait for the Gateway, then save its address:

        ```bash
        kubectl wait gateway/echo-gateway -n echo --for=condition=Programmed --timeout=15m
        export GATEWAY_HOST=$(kubectl get gateway echo-gateway -n echo \
          -o jsonpath='{.status.addresses[0].value}')
        curl -i http://$GATEWAY_HOST/
        ```
