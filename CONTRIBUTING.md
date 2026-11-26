# Contributing Guidelines

Thank you for your interest in contributing to our project. Whether it's a bug report, new feature, correction, or additional
documentation, we greatly value feedback and contributions from our community.

Please read through this document before submitting any issues or pull requests to ensure we have all the necessary
information to effectively respond to your bug report or contribution.

## Reporting Bugs/Feature Requests

We welcome you to use the GitHub issue tracker to report bugs or suggest features.

When filing an issue, please check existing open, or recently closed, issues to make sure somebody else hasn't already
reported the issue. Please try to include as much information as you can. Details like these are incredibly useful:

* A reproducible test case or series of steps
* The version of our code being used
* Any modifications you've made relevant to the bug
* Anything unusual about your environment or deployment

## Propose before you code

Before starting work on a non-trivial change, please open a GitHub issue or discussion to describe what you'd like to do and why. This gives the maintainers a chance to provide early feedback, flag conflicts with in-flight work, and help you avoid spending time on an approach that may not fit the project's direction.

Small, self-contained fixes (typos, minor bug fixes, test improvements) can go straight to a PR. For new features, API changes, refactors, or behavior changes, please discuss with the maintainers first.

## Contributing via Pull Requests

Contributions via pull requests are much appreciated. Before sending us a pull request, please ensure that:

1. You are working against the latest source on the *main* branch.
2. You check existing open, and recently merged, pull requests to make sure someone else hasn't addressed the problem already.
3. You open an issue to discuss any significant work - we would hate for your time to be wasted.

To send us a pull request, please:

1. Fork the repository.
2. Modify the source; please focus on the specific change you are contributing. If you also reformat all the code, it will be hard for us to focus on your change.
3. Ensure local tests pass.
4. Commit to your fork using clear commit messages.
5. Send us a pull request, answering any default questions in the pull request interface.
6. Pay attention to any automated CI failures reported in the pull request, and stay involved in the conversation.

GitHub provides additional document on [forking a repository](https://help.github.com/articles/fork-a-repo/) and
[creating a pull request](https://help.github.com/articles/creating-a-pull-request/).

## Getting started

### Building and testing

Requires the Go version in [`go.mod`](go.mod).

| Command | What it does |
|---------|--------------|
| `make build` | Build the `elb-envoy-remote-infra-provider` binary. |
| `make test` | Run unit tests. |
| `make fmt` | Format the Go code. |
| `make vet` | Run `go vet`, including `test/e2e`. |
| `make lint` | Run `staticcheck`. |
| `make helm-lint` | Lint the Helm chart with the webhook off and on. |
| `make generate` | Regenerate deepcopy code after changing types in `pkg/api/`. |
| `make manifests` | Regenerate the CRD after changing types in `pkg/api/`. It also copies the CRD into the Helm chart. |
| `make mocks` | Regenerate mocks. |
| `make image` | Build the container image for your host architecture. |
| `make docker-push IMG=<registry>/<repo>:<tag>` | Build the image for `linux/amd64` and `linux/arm64`, and push it to your own repository to test on a cluster. See [Test your changes on a cluster](#test-your-changes-on-a-cluster). |
| `make e2e E2E_ROLE_ARN=...` | Run end-to-end tests against a live cluster. See [test/e2e/README.md](test/e2e/README.md). |
| `make tools` | Install `controller-gen`, `crd-ref-docs`, and `staticcheck` at the versions CI uses. |
| `make version` | Show the version of this checkout: `version.txt`, the chart's `appVersion`, and `git describe`. |
| `make release-cut VERSION=1.0.0` | For maintainers. Set the version in `version.txt`, the chart, and the docs links, lint the chart, and build the release artifacts in `_output/release/`. |
| `make ci` | Run the checks CI runs: `generate`, `manifests`, `vet`, `lint`, `test`, `helm-lint`, and `docs-verify`. |

If you change the API in `pkg/api/`, run `make generate manifests api-ref-docs` and commit the result. `make ci` runs these too. Afterwards, `git status` should show no changes to generated files.

### Test your changes on a cluster

Push a test image to your own repository, then install it as in the [Installation Guide](docs/deploy/installation.md), with your image in place of the release image. For Amazon ECR:

```bash
aws ecr get-login-password --region us-east-1 \
  | docker login --username AWS --password-stdin <account>.dkr.ecr.us-east-1.amazonaws.com
make docker-push IMG=<account>.dkr.ecr.us-east-1.amazonaws.com/<repo>:<tag>
```

If you leave out the tag, it's the short commit. To build one architecture only, which is faster, add `PLATFORMS=linux_amd64`. To use [Finch](https://github.com/runfinch/finch) instead of Docker, add `DOCKER=finch`, and log in with `finch login` instead of `docker login`.

### Contributing to docs

The documentation is built with [Material for MkDocs](https://squidfunk.github.io/mkdocs-material/). Pages live in [`docs/`](docs/) and the navigation is in [`mkdocs.yml`](mkdocs.yml).

| Command | What it does |
|---------|--------------|
| `make docs-deps` | Install the docs tools (`mkdocs`, `mkdocs-material`, and `mike`). |
| `make docs-serve` | Preview the docs at http://127.0.0.1:8000. It reloads on every save. |
| `make docs-build` | Build the docs with `--strict`, which fails on broken links. |
| `make docs-publish` | For maintainers. Publish one docs version to the `gh-pages` branch and make it `latest`. On a release branch the version comes from `version.txt` (`0.9.0-rc.1` gives `0.9`). Pass `DOCS_VERSION=1.0` to choose it, or `DOCS_LATEST=false` to leave `latest` alone. |
| `make api-ref-docs` | Regenerate the API reference from `pkg/api/`. |
| `make docs-verify` | Run `api-ref-docs` and `docs-build`, and fail if the API reference isn't committed. |

Run `make docs-verify` before opening a PR that changes the docs or the API types.

The site is published per version, with a version picker in the header. A push to `main` that changes the docs publishes the `dev` version. A release publishes its `MAJOR.MINOR` version (for example `1.0`) and makes it `latest`. Maintainers run the `docs` workflow from the Actions tab on the release branch to do that. Patch releases update their minor version's docs.

The API reference ([`docs/guide/spec.md`](docs/guide/spec.md)) is generated. Don't edit it by hand. `make api-ref-docs` needs [crd-ref-docs](https://github.com/elastic/crd-ref-docs):

```bash
go install github.com/elastic/crd-ref-docs@latest
```

## Finding contributions to work on

Looking at the existing issues is a great way to find something to contribute on. As our projects, by default, use the default GitHub issue labels (enhancement/bug/duplicate/help wanted/invalid/question/wontfix), looking at any 'help wanted' issues is a great place to start.

## Code of Conduct

This project has adopted the [Amazon Open Source Code of Conduct](https://aws.github.io/code-of-conduct).
For more information see the [Code of Conduct FAQ](https://aws.github.io/code-of-conduct-faq) or contact
opensource-codeofconduct@amazon.com with any additional questions or comments.

## Security issue notifications

If you discover a potential security issue in this project we ask that you notify AWS/Amazon Security via our [vulnerability reporting page](https://aws.amazon.com/security/vulnerability-reporting/). Please do **not** create a public github issue.

## Licensing

See the [LICENSE](LICENSE) file for our project's licensing. We will ask you to confirm the licensing of your contribution.
