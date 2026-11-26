<p align="center">
    <img src="assets/images/kubernetes_icon.svg" alt="Kubernetes logo" width="160" />
    <img src="assets/images/aws_load_balancer_icon.svg" alt="Elastic Load Balancing logo" width="160" />
</p>
<p align="center">
    <strong>
        Run
        <a href="https://gateway.envoyproxy.io/">Envoy Gateway</a>
        on AWS managed Envoy proxies with
        <a href="https://docs.aws.amazon.com/elasticloadbalancing/latest/elbproxy/">Elastic Load Balancing (ELB) for Envoy</a>
    </strong>
</p>
<p align="center">
    <a href="https://github.com/aws/elb-envoy-remote-infra-provider/issues">
        <img src="https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat" alt="contributions welcome"/>
    </a>
    <a href="https://github.com/aws/elb-envoy-remote-infra-provider/issues">
        <img src="https://img.shields.io/github/issues-raw/aws/elb-envoy-remote-infra-provider?style=flat" alt="github issues"/>
    </a>
    <img src="https://img.shields.io/badge/API-v1alpha1-orange?style=flat" alt="API version is v1alpha1"/>
    <img src="https://img.shields.io/github/license/aws/elb-envoy-remote-infra-provider?style=flat" alt="apache license"/>
</p>
<p align="center">
    <a href="https://goreportcard.com/report/github.com/aws/elb-envoy-remote-infra-provider">
        <img src="https://goreportcard.com/badge/github.com/aws/elb-envoy-remote-infra-provider" alt="go report card"/>
    </a>
    <img src="https://img.shields.io/github/watchers/aws/elb-envoy-remote-infra-provider?style=social" alt="github watchers"/>
    <img src="https://img.shields.io/github/stars/aws/elb-envoy-remote-infra-provider?style=social" alt="github stars"/>
    <img src="https://img.shields.io/github/forks/aws/elb-envoy-remote-infra-provider?style=social" alt="github forks"/>
</p>

# ELB Envoy Remote Infra Provider

`elb-envoy-remote-infra-provider` is an open source controller that runs the Envoy data plane for [Envoy Gateway](https://gateway.envoyproxy.io/) on ELB for Envoy, instead of on Envoy pods in your cluster. ELB for Envoy runs, scales, and patches the Envoy proxies for you.

Envoy Gateway works the same way it does with in-cluster Envoy: it watches your Gateway API resources and computes Envoy configuration. The controller creates one ELB for Envoy proxy for each Gateway, and that proxy runs the configuration. You keep the Gateway API workflow.

The following diagram shows the flow from your Gateway API resources to a running proxy.

![Envoy Gateway on ELB for Envoy architecture: Envoy Gateway and the elb-envoy-remote-infra-provider sidecar run in the EKS cluster, the sidecar provisions an AWS managed Envoy proxy through the ELB for Envoy API, the proxy gets configuration from Envoy Gateway over xDS, and client traffic flows through the proxy to application pods.](assets/images/architecture.png)

1. You apply Gateway API resources and an `ELBProxyConfiguration`.
2. Envoy Gateway translates them into Envoy configuration.
3. Envoy Gateway sends the Gateway's infrastructure to the sidecar over a Unix socket.
4. The sidecar provisions a managed Envoy proxy through the ELB for Envoy API.
5. The proxy gets its routes and clusters from Envoy Gateway over xDS.
6. Clients connect to the Gateway address, and the proxy sends traffic to your pods.

For the full picture, see [How it works](how-it-works.md).

## What it does

- **Creates one ELB for Envoy proxy for each Gateway.** Attach an [`ELBProxyConfiguration`](guide/overview.md) to a Gateway or GatewayClass, and the controller creates and manages the proxy. With merged gateways, it creates one proxy for each GatewayClass.
- **Keeps Envoy Gateway as the control plane.** The proxy gets its routes and clusters from Envoy Gateway over xDS, so HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, and UDPRoute are all supported. A few Envoy Gateway features differ. See [Compatibility](compatibility.md#envoy-gateway-features).
- **Manages networking.** By default, it discovers subnets, creates security groups for the proxy, and opens the node security group rules the proxy needs to reach your pods.
- **Publishes the Gateway address.** The proxy's DNS name becomes the Gateway's address.
- **Cleans up.** Deleting a Gateway deletes its proxy and the security groups the controller created.

## Getting started

1. [Install the controller and Envoy Gateway](deploy/installation.md).
2. [Deploy your first Gateway on an AWS managed proxy](examples/getting_started.md).
3. Tune the proxy with [`ELBProxyConfiguration`](guide/overview.md) and the [controller flags](deploy/configurations.md).

Check [Compatibility](compatibility.md) for supported versions, and [Troubleshooting](troubleshooting/index.md) if you encounter issues.

## Requirements

- A Kubernetes cluster on Amazon EC2, such as Amazon EKS, with a Kubernetes version supported by your Envoy Gateway release
- Gateway API v1.6 or later standard channel CRDs, including `ListenerSet`
- Envoy Gateway, configured as a remote infrastructure provider, with its leader election disabled
- Pod IPs routable in the virtual private cloud (VPC), for example with the Amazon VPC CNI

See [Compatibility](compatibility.md#platform-requirements) for details.
