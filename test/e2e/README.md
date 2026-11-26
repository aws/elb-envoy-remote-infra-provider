# ELBProxy Gateway Extension — E2E Tests

End-to-end tests for the `elb-envoy-remote-infra-provider` controller. These tests
exercise the controller against a live Kubernetes cluster — they do **not**
provision the cluster or install Envoy Gateway / the extension. They assume
the following is already running and reachable from the test runner:

- A Kubernetes cluster the test runner has admin access to (via
  `KUBECONFIG` or in-cluster service account). Tests create cluster-scoped
  `GatewayClass` objects and namespaces.
- Envoy Gateway with the ELBProxy extension wired in as a remote
  infrastructure provider (see `k8s/envoy-gateway-values.yaml`, or the
  sidecar injection webhook in the Installation Guide).
- An IAM role ARN for `ELBProxyConfiguration.spec.roleArn`, in the same account as
  the proxy. Every test provisions an `ELBProxyConfiguration`, so this is
  required — pass it via `-role-arn` or the `E2E_ROLE_ARN` env var.
- The extension's IAM, networking, and tagging configuration in place
  (VPC ID, subnets, role ARN, etc).

## Test isolation

Tests run in parallel (`t.Parallel()`, capped by `go test -parallel`), so no
two tests share a resource. Each test creates:

| Resource | Where | Name |
| --- | --- | --- |
| `Namespace` | cluster | `elbproxy-e2e-<test>-<random>` |
| `EnvoyProxy` (access logging disabled, `spec.ipFamily` from `-ip-family`) | `-envoyproxy-namespace` | `elbproxy-e2e-<random>` |
| `GatewayClass` (`controllerName` from `-controller-name`, `parametersRef` to the EnvoyProxy) | cluster | same as its EnvoyProxy |
| GatewayClass-scoped `ELBProxyConfiguration`s, when the test has one | `-envoyproxy-namespace` | `elbproxy-e2e-<random>` |
| Everything else: backends, `Gateway`s, routes, Gateway-scoped `ELBProxyConfiguration`s | the test's namespace | fixed (`elbproxy-e2e-gateway`, `elbproxy-e2e-proxy`, ...) |

Random names also keep two runs against the same cluster apart. Because each
test has its own GatewayClass, a GatewayClass-scoped `ELBProxyConfiguration` or a
`mergeGateways` EnvoyProxy only affects that test's Gateways.

One thing is shared: the node security group rules for backend ports. A test that
checks a port gets opened needs a backend port no other test uses (see
`resources/consts.go`), or a parallel test could open it instead.

Every object is labeled `elbproxy-e2e=<test>`. On completion (unless
`-cleanup=false`) each test deletes what it created: Gateways first, so the
`ELBProxyConfiguration` finalizer can release, then everything else in
reverse creation order, ending with the namespace. To find leftovers, for
example after `-cleanup=false` or an interrupted run:

```bash
kubectl get namespaces,gatewayclasses -l elbproxy-e2e
kubectl get envoyproxies,elbproxyconfigurations -n envoy-gateway-system -l elbproxy-e2e
```

The EnvoyProxy is created as an unstructured object with strict server-side
field validation, rather than from the Envoy Gateway Go models, which
lag the cluster CRD. Access logging has to be disabled because the controller
rejects Envoy Gateway's default stdout file sink.

## Building test resources

Test resources are built in Go, following the AWS Load Balancer Controller
gateway e2e tests (`test/e2e/gateway/test_resources` in that repo):

- `resources/shared_resource_definitions.go` has `Build*` functions that
  return objects with no namespace: backends (`BuildEchoDeploymentSpec`,
  `BuildGRPCServiceSpec`, `BuildTLSSecret`, ...), `BuildEnvoyProxySpec`,
  `BuildGatewayClassSpec`, `BuildGatewaySpec`, the route builders
  (`BuildHTTPRoute`, `BuildTLSRoute`, ...), and `BuildELBProxyConfiguration` /
  `BuildGatewayClassELBProxyConfiguration`.
- `resources/resource_stack.go` has `ResourceStack`. `Deploy` allocates the
  test's namespace, places every object without a namespace in it, and creates
  everything in dependency order, tracking each object for cleanup.
  `WaitUntilReady` waits until every `ELBProxyConfiguration` is Accepted, every
  Gateway is Programmed with an address, every ListenerSet is Accepted, and every
  route attached to a Gateway is Accepted. Requests to routes attached to a
  ListenerSet are retried instead.
- `tests/test_helper.go` has `deployHTTP`, `deployGRPC`, `deployTCP`,
  `deployUDP`, `deployTLSPassthrough`, and `deployTLSTerminate` (like LBC's
  `ALBTestStack.DeployHTTP`). Each deploys a backend behind one Gateway with the
  listeners and routes the test passes, plus a Gateway-scoped
  `ELBProxyConfiguration`.

`ELBProxyConfiguration` builders fill the fields a test leaves empty from the
suite flags:

- `roleArn` from `-role-arn` (required).
- `subnets` from `-subnet-ids`. With no subnets the field is omitted, so the
  controller uses subnet auto-discovery.
- `ipv4Addressability` / `ipv6Addressability` from `-ip-family`: `IPv4` →
  `ipv4=internet-facing, ipv6=off`; `IPv6` → `ipv4=internet-facing,
  ipv6=internet-facing` (both, since the proxy fleet runs in dual-stack VPC
  subnets).

A basic test looks like:

```go
stack := deployHTTP(t, s, "httproute-basic", httpListeners(),
	[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
	elbproxyv1alpha1.ELBProxyConfigurationSpec{})
stack.WaitUntilReady(t, s)

s.MakeRequestAndExpectEventuallyConsistentResponse(t,
	framework.HTTPRequest{Host: stack.GetGatewayAddress(), Path: resources.DefaultPathPrefix},
	framework.HTTPExpectation{StatusCode: 200},
)
```

Tests with other shapes fill a `resources.ResourceStack` directly. For
example, `ELBProxyConfigurationMerged` has two Gateways and a
GatewayClass-scoped proxy.

## Running

Use the single-package path `./test/e2e` (not `./test/e2e/...`). With a
multi-package pattern, `go test` buffers each package's output until the
package finishes, so live progress logging never appears. `./test/e2e` is the
only package with tests anyway; its build transitively compiles `framework`,
`resources`, and `tests`.

```bash
# All tests
go test -tags e2e ./test/e2e -v -timeout 240m \
    -role-arn arn:aws:iam::123456789012:role/my-proxy-role

# A single test by ShortName
go test -tags e2e ./test/e2e -v -timeout 30m \
    -role-arn arn:aws:iam::123456789012:role/my-proxy-role \
    -run-test HTTPRouteBasic

# Provide the ARN via the environment instead of a flag
export E2E_ROLE_ARN=arn:aws:iam::123456789012:role/my-proxy-role
go test -tags e2e ./test/e2e -v -timeout 240m

# Run against an IPv6 cluster
go test -tags e2e ./test/e2e -v -timeout 240m \
    -role-arn arn:aws:iam::123456789012:role/my-proxy-role \
    -ip-family IPv6

# Pin specific subnets instead of using auto-discovery
go test -tags e2e ./test/e2e -v -timeout 240m \
    -role-arn arn:aws:iam::123456789012:role/my-proxy-role \
    -subnet-ids subnet-abc,subnet-def
```

> Note: place test flags (`-run-test`, `-role-arn`, ...) **after**
> the `./test/e2e` package path. `go test` treats flags before the package
> path as build flags, which silently resolves the wrong package.

### Running tests in parallel

`TestE2E` runs every test case as a parallel subtest, so tests run
concurrently by default. `-parallel N` caps how many run at once; without it,
Go uses the number of CPUs. Each running test has its own proxy fleet in AWS,
so keep `N` within your account's proxy quota.

```bash
# All tests, at most 6 at a time
go test -tags e2e ./test/e2e -v -timeout 240m -parallel 6 \
    -role-arn arn:aws:iam::123456789012:role/my-proxy-role

# The same through make (E2E_PARALLEL defaults to 4, E2E_TIMEOUT to 240m)
make e2e E2E_ROLE_ARN=arn:aws:iam::123456789012:role/my-proxy-role E2E_PARALLEL=6

# Several tests in parallel: select subtests with -run
go test -tags e2e ./test/e2e -v -timeout 120m -parallel 3 \
    -run 'TestE2E/^(HTTPRouteBasic|TCPRouteBasic|UDPRouteBasic)$' \
    -role-arn arn:aws:iam::123456789012:role/my-proxy-role
```

- `-run-test` (and `E2E_RUN_TEST`) selects exactly one test, so nothing runs in
  parallel. Use `-run` to select several.
- `-timeout` applies to the whole run, not to each test. With `-parallel 4`, a
  full run goes through the tests four at a time, so give it well over 30m (the
  Makefile uses 240m).
- Output from concurrent tests is interleaved. Every live log line carries the
  test name, so you can follow one test:

  ```bash
  make e2e E2E_ROLE_ARN=... 2>&1 | tee e2e.log
  grep 'TestE2E/HTTPRouteBasic' e2e.log
  ```

## Logging

The harness logs progress **live** to stderr (timestamped, prefixed with the
test name) rather than through `t.Log`, which Go buffers until a test finishes.
This means you see resource creates, condition waits, HTTP probe attempts, and
cleanup as they happen — useful since waiting on cloud resources can take
minutes. Long polling loops (Gateway/Route/ELBProxyConfiguration readiness, HTTP probe)
emit a heartbeat roughly every 15–30s with the current blocking reason, so a
slow step never looks like a hang.

For this to actually stream, run the **single** test package `./test/e2e`. If
you pass a multi-package pattern like `./test/e2e/...`, `go test` buffers every
package's stdout/stderr (even direct stderr writes) until the package finishes,
and all the live logging collapses into one dump at the end.

## Flags

| Flag                    | Default                                         | Description                                                                                           |
| ----------------------- | ----------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `-controller-name`      | `gateway.envoyproxy.io/gatewayclass-controller` | `controllerName` of the GatewayClass each test creates.                                               |
| `-envoyproxy-namespace` | `envoy-gateway-system`                          | Envoy Gateway install namespace. Each test's EnvoyProxy and GatewayClass-scoped `ELBProxyConfiguration`s are created here. |
| `-run-test`             | (empty, all tests)                              | If set, only run the test with this `ShortName`.                                                      |
| `-cleanup`              | `true`                                          | Tear down resources created by each test on completion.                                               |
| `-role-arn`             | env `E2E_ROLE_ARN`                              | IAM role ARN used by `ELBProxyConfiguration.spec.roleArn`. Required.                                  |
| `-subnet-ids`           | env `E2E_SUBNET_IDS`                            | Optional comma-separated subnet IDs. Omit to use controller subnet auto-discovery.                    |
| `-ip-family`            | env `E2E_IP_FAMILY`, else `IPv4`                | Cluster IP family (`IPv4` or `IPv6`). Drives ELBProxyConfiguration addressability and the EnvoyProxy `spec.ipFamily`. |
| `-cluster-name`         | env `E2E_CLUSTER_NAME`                          | Cluster name the controller runs with. Only needed for AWS-side verification; those checks are skipped when unset.  |
| `-aws-region`           | env `E2E_AWS_REGION`, else `AWS_REGION`         | AWS region for the read-only verification client.                                                     |
| `-aws-vpc-id`           | env `E2E_AWS_VPC_ID`                            | VPC ID scoping AWS security-group verification. Optional.                                             |
| `-aws-api-endpoint`     | env `E2E_AWS_API_ENDPOINT`                      | ELB Proxy API endpoint for the verifier. Optional; empty uses the prod endpoint.                      |
| `-aws-tagging-api-endpoint` | env `E2E_AWS_TAGGING_API_ENDPOINT`          | Resource Groups Tagging API endpoint the verifier finds proxies with. Optional; empty uses the prod endpoint. |

For a gamma run, set both endpoints to their gamma URLs, and point the controller at the same two endpoints with
`--aws-api-endpoints`. 

## Test layout

```
test/e2e/
├── e2e_test.go            # TestE2E entry point (runs registered tests in parallel)
├── framework/             # Test harness (build tag: e2e)
│   ├── framework.go       # Suite + flags, NewSuite, RunTest, IP-family helpers
│   ├── apply.go           # CreateObject (create + track for cleanup)
│   ├── cleanup.go         # Cleanup ordering (Gateways first, then reverse creation order)
│   ├── wait.go            # Condition pollers (Gateway/Route/ELBProxyConfiguration ready)
│   ├── http.go, grpc.go, tcp.go, tls.go, udp.go  # Protocol probe helpers
│   ├── aws.go             # Read-only AWS verifier (optional)
│   ├── log.go, scheme.go, util.go
├── resources/             # Resource builders (LBC test_resources style)
│   ├── consts.go          # Names, ports, images
│   ├── shared_resource_definitions.go  # Build* functions
│   └── resource_stack.go  # ResourceStack: Deploy, WaitUntilReady
└── tests/                 # Per-test definitions (one file per scenario)
    ├── tests.go           # E2ETests []framework.TestCase registry
    ├── test_helper.go     # deployHTTP, deployGRPC, ... stack helpers
    ├── envoyextensionpolicy_wasm_http.go  # Wasm module downloaded from Envoy Gateway (needs EG internet access)
    ├── httproute_basic.go
    ├── securitypolicy_extauth.go  # ext_authz backend port opened for a policy
    └── ... (grpc/tcp/tls/udp routes, elbproxyconfiguration_* scenarios)
```

To add a new test:

1. Add a `*.go` file in `tests/` that registers a `TestCase` in `init()`.
2. In `Test`, deploy a stack with a `deploy*` helper, or fill a
   `resources.ResourceStack` from `Build*` functions and call `Deploy`. Add a
   `Build*` function to `resources/` if you need a new kind of object.
3. Run `go test -tags e2e ./test/e2e -run-test <ShortName> ...`.
