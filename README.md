<p align="center">
    <img src="docs/assets/images/kubernetes_icon.svg" alt="Kubernetes logo" width="160" />
    <img src="docs/assets/images/aws_load_balancer_icon.svg" alt="Elastic Load Balancing logo" width="160" />
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

## elb-envoy-remote-infra-provider

`elb-envoy-remote-infra-provider` is an [Envoy Gateway](https://gateway.envoyproxy.io/) remote infrastructure provider for AWS. Envoy Gateway keeps translating your Gateway API resources into Envoy configuration, and this project runs the Envoy data plane on AWS-managed proxies instead of pods in your cluster.

- It runs as a sidecar in the Envoy Gateway pod and creates an AWS managed Envoy proxy for each Gateway that has an `ELBProxyConfiguration` attached.
- The proxy gets its routes and clusters from Envoy Gateway over xDS, and sends traffic directly to your pods.
- It manages the proxy's subnets and security groups, and publishes the proxy's DNS name as the Gateway's address.


## Documentation

Check out our [Live Docs](https://aws.github.io/elb-envoy-remote-infra-provider/).

- [Installation Guide](docs/deploy/installation.md)
- [Getting Started](docs/examples/getting_started.md)
- [How it works](docs/how-it-works.md)
- [Compatibility](docs/compatibility.md)

## API version

The API version is `v1alpha1`. Check the release notes before you upgrade.

## Contributing

We welcome contributions! For non-trivial changes (new features, API changes, refactors), please open an issue or discussion before submitting a PR so the maintainers can provide early feedback. See [CONTRIBUTING.md](CONTRIBUTING.md) for full details.

## Security

To report a security issue, use the [AWS vulnerability reporting page](https://aws.amazon.com/security/vulnerability-reporting/). Don't open a public issue. See [CONTRIBUTING.md](CONTRIBUTING.md#security-issue-notifications) for details.

## Code of conduct

This project has adopted the [Amazon Open Source Code of Conduct](https://aws.github.io/code-of-conduct). See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## License

This project is licensed under the Apache-2.0 License.
