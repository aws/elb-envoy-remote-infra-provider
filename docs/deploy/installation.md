# Installation Guide

`elb-envoy-remote-infra-provider` connects [Envoy Gateway](https://gateway.envoyproxy.io/) to ELB for Envoy (AWS managed Envoy). It runs as a sidecar inside the Envoy Gateway pod. Envoy Gateway sends it the infrastructure for each Gateway, and the sidecar creates and manages the matching AWS proxy.

The Helm chart installs:

- The `ELBProxyConfiguration` CRD.
- RBAC for the sidecar, bound to the Envoy Gateway service account.
- Optionally, a mutating webhook that injects the sidecar into the Envoy Gateway pod.

The chart does not install Envoy Gateway. You install and configure it yourself in [Step 4](#step-4-install-envoy-gateway).

!!! tip
    If you already run Envoy Gateway with in-cluster Envoy, don't switch your existing install to remote. Follow [Migrating from in-cluster Envoy](migration.md) instead.

## Choose how to add the sidecar

The controller runs as an extra container (a sidecar) in the Envoy Gateway pod. You can add that container in one of two ways:

- **Envoy Gateway Helm values (default).** You put the sidecar definition in the Helm values that you use to install Envoy Gateway. No other component runs in your cluster. The file [`k8s/envoy-gateway-values.yaml`](https://github.com/aws/elb-envoy-remote-infra-provider/blob/v1.0.0/k8s/envoy-gateway-values.yaml) contains the definition.
- **Webhook.** You install a webhook from this chart. When Envoy Gateway's pod is created, the webhook adds the sidecar. Choose this only if you can't change Envoy Gateway's Helm values. The webhook needs its own deployment and TLS certificate. With the default `failurePolicy: Fail`, Envoy Gateway pods can't be created while the webhook is down.

The sidecar behaves the same in both options. Steps 3 and 4 have a tab for each option. Use the same option in both steps.

## Prerequisites

- A Kubernetes cluster running a version supported by Envoy Gateway **v1.10** (v1.34 to v1.37), with nodes that are Amazon EC2 instances in the same virtual private cloud (VPC) as the proxy. Amazon EKS clusters meet this. For other clusters, see the [platform requirements](../compatibility.md#platform-requirements).
- `kubectl`, `helm` (v3), and the AWS CLI.
- Envoy Gateway **v1.10**. Controller v1.0.0 is built and tested against it.
- The VPC ID of your cluster.

The chart and the controller image come from Amazon ECR. The IAM policy and the Envoy Gateway values file come from this repository, so clone the release you install and run the commands from its root:

```bash
export VERSION=<release version, for example 1.0.0>
git clone --branch v$VERSION https://github.com/aws/elb-envoy-remote-infra-provider.git
cd elb-envoy-remote-infra-provider
```

The examples also use these variables:

```bash
export CLUSTER_NAME=my-cluster
export AWS_REGION=us-west-2
export AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
export VPC_ID=$(aws eks describe-cluster --name $CLUSTER_NAME \
  --query cluster.resourcesVpcConfig.vpcId --output text)
```

If your cluster isn't on Amazon EKS, set `VPC_ID` to the ID of the VPC that your nodes run in.

## Step 1: Create the controller IAM policy

The sidecar calls the ELB and EC2 APIs. Download [`k8s/iam-policy.json`](https://github.com/aws/elb-envoy-remote-infra-provider/blob/v1.0.0/k8s/iam-policy.json) and replace the `${ClusterName}` placeholder with your cluster name:

```bash
sed "s/\${ClusterName}/$CLUSTER_NAME/" k8s/iam-policy.json > iam-policy.json

aws iam create-policy \
  --policy-name ELBEnvoyRemoteInfraProviderPolicy \
  --policy-document file://iam-policy.json
```

The policy scopes write access to resources tagged `gateway.elbproxy.aws/managed-by`, which the controller adds to everything it creates. The one exception is node security groups. The controller can add ingress rules to security groups tagged `kubernetes.io/cluster/<cluster-name>`.

## Step 2: Grant the policy to the Envoy Gateway service account

The sidecar shares the Envoy Gateway pod, so it runs as Envoy Gateway's service account (`envoy-gateway` in `envoy-gateway-system`). Give that service account the policy from Step 1.

=== "EKS Pod Identity"

    ```bash
    aws iam create-role \
      --role-name ELBEnvoyRemoteInfraProviderRole \
      --assume-role-policy-document '{
        "Version": "2012-10-17",
        "Statement": [{
          "Effect": "Allow",
          "Principal": {"Service": "pods.eks.amazonaws.com"},
          "Action": ["sts:AssumeRole", "sts:TagSession"]
        }]
      }'

    aws iam attach-role-policy \
      --role-name ELBEnvoyRemoteInfraProviderRole \
      --policy-arn arn:aws:iam::$AWS_ACCOUNT_ID:policy/ELBEnvoyRemoteInfraProviderPolicy

    aws eks create-pod-identity-association \
      --cluster-name $CLUSTER_NAME \
      --namespace envoy-gateway-system \
      --service-account envoy-gateway \
      --role-arn arn:aws:iam::$AWS_ACCOUNT_ID:role/ELBEnvoyRemoteInfraProviderRole
    ```

    Pod Identity requires the EKS Pod Identity Agent add-on on the cluster. The association is stored in EKS, not on the service account, so it survives Envoy Gateway reinstalls.

=== "IAM roles for service accounts (IRSA)"

    Create a role that trusts your cluster's OIDC provider for `system:serviceaccount:envoy-gateway-system:envoy-gateway`, and attach the policy from Step 1.

    You annotate the service account in [Step 4](#step-4-install-envoy-gateway), after Envoy Gateway creates it. Envoy Gateway's Helm chart has no value for service account annotations, so the annotation has to be added with `kubectl`.

## Step 3: Install the Helm chart

=== "Envoy Gateway Helm values (default)"

    The chart installs only the CRD and the sidecar RBAC. No values are required:

    ```bash
    helm install elb-envoy-remote-infra-provider oci://public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider-chart \
      --version $VERSION \
      -n elb-envoy-remote-infra-provider-system --create-namespace
    ```

    The following table lists the chart values for this option. You set the sidecar's own settings in Envoy Gateway's values instead, in [Step 4](#step-4-install-envoy-gateway). For all sidecar flags, see [Configurations](configurations.md).

    | Value | Default | Description |
    |-------|---------|-------------|
    | `envoyGatewayNamespace` | `envoy-gateway-system` | Namespace where Envoy Gateway runs. Used for the RBAC binding. If Envoy Gateway runs in another namespace, set it, for example `--set envoyGatewayNamespace=<namespace>`. |
    | `rbac.serviceAccountName` | `envoy-gateway` | Envoy Gateway's service account, which the sidecar runs as. |

=== "Webhook"

    Install the chart **before** Envoy Gateway. The webhook only adds the sidecar when a pod is created, so Envoy Gateway pods that already exist won't get it until they restart.

    ```bash
    helm install elb-envoy-remote-infra-provider oci://public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider-chart \
      --version $VERSION \
      -n elb-envoy-remote-infra-provider-system --create-namespace \
      --set webhook.enabled=true \
      --set clusterName=$CLUSTER_NAME \
      --set aws.region=$AWS_REGION \
      --set aws.vpcId=$VPC_ID
    ```

    `clusterName` is required. Set `aws.region` and `aws.vpcId` too: the chart passes them to the sidecar as flags, and the sidecar fails to start if it can't resolve a VPC.

    Wait until the webhook is ready before you install Envoy Gateway:

    ```bash
    kubectl rollout status deployment/elb-envoy-remote-infra-provider \
      -n elb-envoy-remote-infra-provider-system
    ```

    !!! warning
        With the default `failurePolicy: Fail`, Envoy Gateway pods can't be created while no webhook replica is ready, for example during the first install or while a single replica restarts. Keep the webhook running for as long as Envoy Gateway is installed, and run 2 or more replicas (`replicaCount`) in production.

    The following table lists the main chart values for this option. For all sidecar flags, see [Configurations](configurations.md).

    | Value | Default | Description |
    |-------|---------|-------------|
    | `webhook.enabled` | `false` | Install the sidecar-injection webhook. Set it to `true`. |
    | `clusterName` | `""` | Sidecar `--cluster-name`. Required. |
    | `aws.region` | `""` | Sidecar `--aws-region`. If empty, the sidecar uses the `AWS_REGION` environment variable (IRSA sets it). Recommended. |
    | `aws.vpcId` | `""` | Sidecar `--aws-vpc-id`. Recommended. |
    | `webhook.sidecar.extraArgs` | `[]` | Extra flags for the sidecar, for example `--default-tags=team=a,env=b`. |
    | `image.repository` | `public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider` | Image for the webhook and the injected sidecar. |
    | `image.tag` | the release tag, for example `v1.0.0` | Image tag. |
    | `replicaCount` | `1` | Webhook replicas. |
    | `webhook.failurePolicy` | `Fail` | `Fail` blocks Envoy Gateway pod creation while no webhook replica is ready. `Ignore` lets the pod start without the sidecar. |
    | `envoyGatewayNamespace` | `envoy-gateway-system` | Namespace where Envoy Gateway runs. Used for the RBAC binding and to choose which pods get the sidecar. |
    | `rbac.serviceAccountName` | `envoy-gateway` | Envoy Gateway's service account, which the injected sidecar runs as. |

## Step 4: Install Envoy Gateway

Envoy Gateway must send infrastructure to the sidecar over a Unix domain socket instead of creating Envoy deployments in the cluster. This needs a `config` section in Envoy Gateway's Helm values, with leader election disabled. The values file of the default option already includes it. With the webhook, you write it yourself.

!!! note "Why leader election must be disabled"
    Each Envoy Gateway replica sends infrastructure only to the sidecar in its own pod. With Envoy Gateway's leader election on, only the leader sends it, so the other sidecars can act on stale data. With it off, every replica keeps its sidecar up to date. The sidecars then run their own leader election, so only one replica writes to AWS.

=== "Envoy Gateway Helm values (default)"

    [`k8s/envoy-gateway-values.yaml`](https://github.com/aws/elb-envoy-remote-infra-provider/blob/v1.0.0/k8s/envoy-gateway-values.yaml) contains the `config` section and the sidecar: the container, the shared socket volume, and the socket mount on the Envoy Gateway container. Fill in its placeholders:

    ```bash
    sed -e "s|<tag>|v$VERSION|" \
        -e "s|<cluster-name>|$CLUSTER_NAME|" \
        -e "s|<region>|$AWS_REGION|" \
        -e "s|<vpc-id>|$VPC_ID|" \
        k8s/envoy-gateway-values.yaml > eg-values.yaml
    ```

    To set more sidecar flags, such as `--default-tags`, add them to the sidecar's `args` in `eg-values.yaml`. If Envoy Gateway runs in another namespace, also change the `ENVOY_GATEWAY_NAMESPACE` value.

=== "Webhook"

    Save this `config` section as `eg-values.yaml`. Don't add the sidecar: the webhook adds it.

    ```yaml
    config:
      envoyGateway:
        gateway:
          controllerName: gateway.envoyproxy.io/gatewayclass-controller
        provider:
          type: Custom
          custom:
            resource:
              type: Kubernetes
              kubernetes:
                # Required. The sidecar runs its own leader election.
                leaderElection:
                  disable: true
            infrastructure:
              type: Remote
              remote:
                service:
                  unix:
                    # Must match the sidecar's --server-socket-path.
                    path: /var/run/elb-envoy-remote-infra-provider/server.sock
    ```

Then install Envoy Gateway:

```bash
helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.10.0 \
  -n envoy-gateway-system --create-namespace \
  -f eg-values.yaml
```

!!! note "If the Gateway API CRDs are already installed"
    If your cluster already has Gateway API CRDs, for example from the AWS Load Balancer Controller, `helm install` can fail with a CRD conflict. Install the CRDs yourself, then install Envoy Gateway with `--skip-crds`:

    ```bash
    kubectl apply --server-side --force-conflicts \
      -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.1/standard-install.yaml
    helm pull oci://docker.io/envoyproxy/gateway-crds-helm --version v1.10.0
    helm template eg-crds ./gateway-crds-helm-v1.10.0.tgz --set crds.envoyGateway.enabled=true \
      | kubectl apply --server-side --force-conflicts -f -
    helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.10.0 \
      -n envoy-gateway-system --create-namespace --skip-crds \
      -f eg-values.yaml
    ```

    Every Gateway API controller in the cluster shares these CRDs, so check that the others support Gateway API v1.6.1 first.

If you use IAM roles for service accounts (IRSA), annotate the service account Envoy Gateway just created, then restart Envoy Gateway so the pod gets credentials:

```bash
kubectl annotate serviceaccount envoy-gateway -n envoy-gateway-system \
  eks.amazonaws.com/role-arn=arn:aws:iam::$AWS_ACCOUNT_ID:role/ELBEnvoyRemoteInfraProviderRole
kubectl rollout restart deployment envoy-gateway -n envoy-gateway-system
```

!!! warning
    `helm uninstall eg` deletes the service account and its annotation. After you reinstall Envoy Gateway, run both commands again. Pod Identity doesn't have this problem.

If Envoy Gateway is already installed, upgrade it with the same values and restart it. Use the Envoy Gateway version listed for your controller release. See [Upgrading](../compatibility.md#upgrading).

```bash
helm upgrade eg oci://docker.io/envoyproxy/gateway-helm --version v1.10.0 \
  -n envoy-gateway-system -f eg-values.yaml
kubectl rollout restart deployment envoy-gateway -n envoy-gateway-system
```

## Step 5: Verify the installation

Check that the Envoy Gateway pod has the sidecar and that both containers are ready:

```bash
kubectl get pods -n envoy-gateway-system -l control-plane=envoy-gateway \
  -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.initContainers[*].name}{"\n"}{end}'
kubectl get pods -n envoy-gateway-system -l control-plane=envoy-gateway
```

The first command should list `elb-envoy-remote-infra-provider` for each Envoy Gateway pod, and the second should show `2/2` ready. The sidecar is a native sidecar (an init container that keeps running), so it's listed under `initContainers`.

Check the sidecar logs. A healthy sidecar logs `gRPC server listening` and `Successfully acquired lease` (on one replica), with no errors:

```bash
kubectl logs -n envoy-gateway-system deployment/envoy-gateway \
  -c elb-envoy-remote-infra-provider
```

With the webhook, also check that the webhook is running:

```bash
kubectl get deployment -n elb-envoy-remote-infra-provider-system
```

Next, deploy a Gateway by following [Getting Started](../examples/getting_started.md).

## Uninstall

Delete your Gateways first, and wait until they are gone (`kubectl get gateways -A`). The controller removes their AWS proxies and security groups during this time, so don't uninstall Envoy Gateway before it finishes.

Then delete your `ELBProxyConfiguration`s, while the controller is still running. Each one has a finalizer that only the controller removes, so after you uninstall it they can't be deleted:

```bash
kubectl delete elbproxyconfigurations -A --all
```

Then uninstall:

```bash
helm uninstall eg -n envoy-gateway-system
helm uninstall elb-envoy-remote-infra-provider -n elb-envoy-remote-infra-provider-system
```

`helm uninstall eg` leaves the resources that Envoy Gateway's chart creates in its install hooks. Remove them, and delete the namespaces if nothing else runs in them:

```bash
kubectl delete mutatingwebhookconfiguration envoy-gateway-topology-injector.envoy-gateway-system
kubectl delete clusterrolebinding,clusterrole eg-gateway-helm-certgen:envoy-gateway-system
kubectl delete namespace envoy-gateway-system elb-envoy-remote-infra-provider-system
```

!!! note
    Helm doesn't delete CRDs. To remove the `ELBProxyConfiguration` CRD, run
    `kubectl delete crd elbproxyconfigurations.gateway.elbproxy.aws`.
