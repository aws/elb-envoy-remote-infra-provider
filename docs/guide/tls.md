# TLS certificates

The proxy terminates TLS at its Envoy listener. Certificates follow the standard Gateway API model: you store each one in a `kubernetes.io/tls` Secret and reference it from the Gateway listener. Envoy Gateway sends the certificate to the proxy over xDS, so a rotated Secret reaches the proxy without replacing it.

This page covers three ways to get a certificate into that Secret:

- [cert-manager with Let's Encrypt](#cert-manager-with-lets-encrypt): free certificates, issued and renewed through ACME.
- [ACM exportable certificates](#acm-exportable-certificates): certificates from AWS Certificate Manager (ACM), exported into a Secret by AWS Controllers for Kubernetes (ACK).
- [Your own certificate](#your-own-certificate): a certificate and key you already have.

All three end with the same listener configuration, shown in [Bind the certificate to a Gateway](#bind-the-certificate-to-a-gateway).

## Before you start

- Follow [Getting Started](../examples/getting_started.md) first. The examples here use its `elb-proxy-aws` GatewayClass.
- The controller-managed security group opens each **listener port** to `spec.ingressCidrs` (default `0.0.0.0/0`, plus `::/0` with IPv6). An HTTPS listener on port 443 is opened automatically. See [Security groups](overview.md#security-groups).
- Point your domain's DNS at the Gateway's address, which is the proxy's DNS name, for example, with a CNAME record:

    ```bash
    kubectl get gateway <name> -n <namespace> -o jsonpath='{.status.addresses[0].value}'
    ```

## cert-manager with Let's Encrypt

The [cert-manager](https://cert-manager.io/) project issues and renews certificates in the cluster. With Let's Encrypt, the certificates are free and trusted by browsers.

### Install cert-manager

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
kubectl rollout status deployment cert-manager -n cert-manager --timeout=120s
kubectl rollout status deployment cert-manager-webhook -n cert-manager --timeout=120s
```

Enable cert-manager's Gateway API support. The HTTP-01 challenge below needs it:

```bash
kubectl patch deployment cert-manager -n cert-manager --type=json -p='[
  {"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--enable-gateway-api"}
]'
kubectl rollout status deployment cert-manager -n cert-manager --timeout=120s
```

### Create a ClusterIssuer

This issuer answers HTTP-01 challenges through your Gateway:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: you@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-account
    solvers:
      - http01:
          gatewayHTTPRoute:
            parentRefs:
              - name: <gateway-name>
                namespace: <gateway-namespace>
                kind: Gateway
```

!!! note "HTTP-01 requirements"
    Let's Encrypt connects to `http://<your-domain>/.well-known/acme-challenge/...` from the internet. For that to reach the proxy:

    - The proxy must be internet-facing. The default is `internal`, so set `ipv4Addressability: internet-facing` in the `ELBProxyConfiguration`. See [Addressability](overview.md#addressability).
    - The Gateway needs an HTTP listener on port 80, and `spec.ingressCidrs` must allow the internet on it.
    - Your domain's DNS must already point at the Gateway's address.

    For internal proxies or wildcard certificates, use a DNS-01 solver instead. See the [cert-manager ACME documentation](https://cert-manager.io/docs/configuration/acme/).

### Request a certificate

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: gateway-tls
  namespace: <gateway-namespace>
spec:
  secretName: gateway-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - your-domain.example.com
```

After the challenge completes, cert-manager writes the certificate to the `gateway-tls` Secret. Check progress with:

```bash
kubectl get certificate gateway-tls -n <gateway-namespace>
```

`READY` turns `True` once the certificate is issued. Then [bind it to the Gateway](#bind-the-certificate-to-a-gateway) with `certificateRefs` name `gateway-tls`.

### Rotation

Before the certificate expires, cert-manager renews it and updates the Secret in place. Envoy Gateway sends the new certificate to the proxy. Nothing else is needed. See [cert-manager certificate renewal](https://cert-manager.io/docs/usage/certificate/#renewal).

## ACM exportable certificates

AWS Certificate Manager can issue public certificates that can be exported. The [ACK](https://aws-controllers-k8s.github.io/community/) ACM controller requests the certificate and exports it, with its private key, into a Kubernetes Secret. It also keeps the Secret up to date when ACM renews the certificate. For the full walkthrough, see [Secure Kubernetes workloads with ACM certificates](https://docs.aws.amazon.com/acm/latest/userguide/exportable-certificates-kubernetes.html) in the ACM User Guide.

### Enable ACK as an EKS capability

Create an AWS Identity and Access Management (IAM) role for the ACK capability:

```bash
cat > ack-trust-policy.json << 'EOF'
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"Service": "capabilities.eks.amazonaws.com"},
      "Action": ["sts:AssumeRole", "sts:TagSession"]
    }
  ]
}
EOF

aws iam create-role \
  --role-name ACKCapabilityRole \
  --assume-role-policy-document file://ack-trust-policy.json

cat > ack-acm-policy.json << EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "RequestCertificates",
      "Effect": "Allow",
      "Action": "acm:RequestCertificate",
      "Resource": "*"
    },
    {
      "Sid": "ManageCertificates",
      "Effect": "Allow",
      "Action": [
        "acm:DescribeCertificate",
        "acm:UpdateCertificateOptions",
        "acm:DeleteCertificate",
        "acm:ExportCertificate",
        "acm:AddTagsToCertificate",
        "acm:RemoveTagsFromCertificate",
        "acm:ListTagsForCertificate"
      ],
      "Resource": "arn:aws:acm:$AWS_REGION:$AWS_ACCOUNT_ID:certificate/*"
    }
  ]
}
EOF
aws iam put-role-policy \
  --role-name ACKCapabilityRole \
  --policy-name ACKCertificateManager \
  --policy-document file://ack-acm-policy.json
```
This policy lets the ACK controller request public certificates, and manage and export the certificates in your account and Region. It is based on the [recommended policy](https://github.com/aws-controllers-k8s/acm-controller/blob/main/config/iam/recommended-inline-policy) of the ACK ACM controller, without the permissions for imported certificates, private certificates, and ACME endpoints. If the controller reports `AccessDenied` for another ACM action, add that action to the policy.

Create the capability and wait for it to become `ACTIVE`:

```bash
aws eks create-capability \
  --region $AWS_REGION \
  --cluster-name $CLUSTER_NAME \
  --capability-name ack \
  --type ACK \
  --role-arn arn:aws:iam::$AWS_ACCOUNT_ID:role/ACKCapabilityRole \
  --delete-propagation-policy RETAIN

aws eks describe-capability \
  --region $AWS_REGION \
  --cluster-name $CLUSTER_NAME \
  --capability-name ack \
  --query 'capability.status' --output text
```

Check that the ACM resource types are available:

```bash
kubectl api-resources | grep acm.services.k8s.aws
```

### Request and export the certificate

Create an empty `kubernetes.io/tls` Secret for the export, and a `Certificate` that exports into it:

```yaml
apiVersion: v1
kind: Secret
type: kubernetes.io/tls
metadata:
  name: acm-gateway-tls
  namespace: <gateway-namespace>
data:
  tls.crt: ""
  tls.key: ""
---
apiVersion: acm.services.k8s.aws/v1alpha1
kind: Certificate
metadata:
  name: gateway-cert
  namespace: <gateway-namespace>
spec:
  domainName: your-domain.example.com
  options:
    certificateTransparencyLoggingPreference: ENABLED
  exportTo:
    namespace: <gateway-namespace>
    name: acm-gateway-tls
    key: tls.crt
```

ACM issues a public certificate only after you validate the domain, by DNS or email. See [ACM domain validation](https://docs.aws.amazon.com/acm/latest/userguide/dns-validation.html). Once it's issued, the ACK controller writes the certificate and private key to `acm-gateway-tls`. Then [bind it to the Gateway](#bind-the-certificate-to-a-gateway) with `certificateRefs` name `acm-gateway-tls`.

### Rotation

ACM renews eligible certificates before they expire. With `exportTo` set, the ACK controller updates the Secret with the renewed certificate, and Envoy Gateway sends it to the proxy. Nothing else is needed.

## Your own certificate

If you already have a certificate and private key, such as one from your own certificate authority, create the Secret yourself. Put the full chain in the certificate file:

```bash
kubectl create secret tls gateway-tls -n <gateway-namespace> \
  --cert=tls.crt --key=tls.key
```

Then [bind it to the Gateway](#bind-the-certificate-to-a-gateway) with `certificateRefs` name `gateway-tls`.

### Rotation

To rotate the certificate, update the Secret with the new files:

```bash
kubectl create secret tls gateway-tls -n <gateway-namespace> \
  --cert=new-tls.crt --key=new-tls.key --dry-run=client -o yaml | kubectl apply -f -
```

Envoy Gateway sends the new certificate to the proxy, which serves it within seconds. The proxy isn't replaced.

## Bind the certificate to a Gateway

Reference the Secret from an `HTTPS` listener:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway
  namespace: <gateway-namespace>
spec:
  gatewayClassName: elb-proxy-aws
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: gateway-tls  # or acm-gateway-tls
```

The proxy serves routes attached to this listener with the certificate. The same `certificateRefs` work on a `TLS` listener with `mode: Terminate` for TLSRoute. For `mode: Passthrough`, the proxy forwards TLS to the backend unchanged and needs no certificate.

Check that the Gateway accepted the certificate. `ResolvedRefs` is `False` in three cases: the Secret is missing, it isn't of type `kubernetes.io/tls`, or it's in another namespace without a `ReferenceGrant`:

```bash
kubectl get gateway my-gateway -n <gateway-namespace> \
  -o jsonpath='{range .status.listeners[*]}{.name}{": "}{range .conditions[*]}{.type}={.status} {end}{"\n"}{end}'
```

Then test the connection:

```bash
curl -v https://your-domain.example.com/
```

!!! tip "Secret in another namespace"
    To use a Secret from a different namespace, add `namespace` to the `certificateRefs` entry. Then create a Gateway API `ReferenceGrant` in the Secret's namespace that allows Gateways from the Gateway's namespace to reference Secrets.
