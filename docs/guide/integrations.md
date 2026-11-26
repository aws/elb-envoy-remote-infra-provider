# AWS integrations

This page covers two AWS services that you can connect to a proxy through its `ELBProxyConfiguration`:

- [AWS WAF](#aws-waf): filter the proxy's HTTP traffic with your own web ACL.
- [Customer managed KMS keys](#customer-managed-kms-keys): encrypt the proxy's configuration with your own AWS KMS key.

Both are off by default. Each one needs extra permissions in the controller IAM policy ([`k8s/iam-policy.json`](https://github.com/aws/elb-envoy-remote-infra-provider/blob/v1.0.0/k8s/iam-policy.json)). If you don't use a service, you can remove its statements from the policy.

## AWS WAF

A proxy has no AWS WAF protection by default. To protect it, you create your own web ACL in AWS WAF and give its ARN to the proxy. The ELB for Envoy service never creates, changes, or deletes a web ACL. It only records which web ACL is associated with the proxy.

### Set a web ACL when you create the proxy

Set the web ACL's ARN in the `waf.web_acl_arn` attribute:

```yaml
spec:
  attributes:
    - key: waf.web_acl_arn
      value: arn:aws:wafv2:us-west-2:111122223333:regional/webacl/my-acl/a1b2c3d4-5678-90ab-cdef-EXAMPLE11111
```

The web ACL must be a regional web ACL in the same Region as the proxy. When the controller creates the proxy, the service associates the web ACL during the create call. If the ARN is wrong, the web ACL doesn't exist, or the controller lacks permission, the proxy isn't created, and the Gateway gets a `FailedDeployModel` event with the error.

The service applies the web ACL's rules to requests before they reach your backends. A request that a rule blocks gets `403 Forbidden` from the proxy.

### Permissions

The service associates the web ACL with the controller's credentials, so the controller IAM policy needs these two statements:

```json
{
    "Sid": "WAFAssociateWebACL",
    "Effect": "Allow",
    "Action": "wafv2:AssociateWebACL",
    "Resource": "arn:aws:wafv2:*:*:regional/webacl/*/*"
},
{
    "Sid": "ELBProxyWafAssociation",
    "Effect": "Allow",
    "Action": [
        "elasticloadbalancing:CreateProxyWafAssociation",
        "elasticloadbalancing:DeleteProxyWafAssociation",
        "elasticloadbalancing:GetProxyWafAssociation",
        "elasticloadbalancing:ListProxyWafAssociations"
    ],
    "Resource": "*"
}
```

- **`WAFAssociateWebACL`** lets the controller use a web ACL. AWS WAF checks `wafv2:AssociateWebACL` on the web ACL itself, the same way it does when you protect an Application Load Balancer. The resource matches any regional web ACL. To allow only specific web ACLs, replace `regional/webacl/*/*` with their names, for example `regional/webacl/my-acl/*`.
- **`ELBProxyWafAssociation`** lets the controller attach a web ACL to the proxy, on the ELB for Envoy side.

Without these statements, creating a proxy that sets `waf.web_acl_arn` fails with `is not authorized to perform: wafv2:AssociateWebACL` or `elasticloadbalancing:CreateProxyWafAssociation`. A proxy that doesn't set it doesn't need them.

### Change or remove the web ACL

The service doesn't change `waf.web_acl_arn` with an attribute update. To change or remove the web ACL on an existing proxy, call AWS WAF with the proxy's ARN, then update `spec.attributes` to match:

1. Get the proxy ARN:
    ```bash
    kubectl get elbproxyconfiguration <name> -n <namespace> \
      -o jsonpath='{.status.proxies[0].proxyArn}{"\n"}'
    ```
2. Change or remove the web ACL:
    ```bash
    # Use a different web ACL. The new one replaces the old one.
    aws wafv2 associate-web-acl --web-acl-arn <new-web-acl-arn> --resource-arn <proxy-arn>

    # Remove WAF protection.
    aws wafv2 disassociate-web-acl --resource-arn <proxy-arn>
    ```
3. If `spec.attributes` has `waf.web_acl_arn`, set it to the new ARN, or remove the entry. If the spec doesn't have the key, you don't need to change anything.

You run these commands with your own credentials, so you need `wafv2:AssociateWebACL` or `wafv2:DisassociateWebACL`. The controller doesn't need them for this.

!!! warning "Don't change `waf.web_acl_arn` in the spec only"
    If the value in the spec doesn't match the web ACL on the proxy, the controller stops with the error `attribute "waf.web_acl_arn" is read-only`, and the Gateway gets a `FailedDeployModel` event. Until the spec matches again, the controller doesn't apply the proxy's other changes either, such as network and Envoy configuration updates. It also retries about twice a second, and each retry calls the ELB for Envoy API, so fix the mismatch quickly.

### Check which web ACL protects a proxy

```bash
aws wafv2 get-web-acl-for-resource --resource-arn <proxy-arn> --query 'WebACL.Name'
```

To list the proxies that use a web ACL, pass `--resource-type ELB_PROXY`. Without it, AWS WAF lists only Application Load Balancers:

```bash
aws wafv2 list-resources-for-web-acl --web-acl-arn <web-acl-arn> --resource-type ELB_PROXY
```

### Deleting a proxy or a web ACL

Deleting the Gateway deletes the proxy and its association. Your web ACL stays in your account. AWS WAF doesn't delete a web ACL that is still associated with a proxy, so remove it from the proxy first.

### When AWS WAF is unavailable

By default, the proxy blocks requests when it can't reach AWS WAF. To let them through instead, set `waf.fail_open.enabled` to `true`. Unlike `waf.web_acl_arn`, you can change this attribute at any time in `spec.attributes`.

## Customer managed KMS keys

The service stores each proxy's configuration encrypted. By default, it uses a key that the service owns. To use your own AWS KMS key instead, set its ARN in `spec.customerKmsKeyArn`:

```yaml
spec:
  customerKmsKeyArn: arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab
```

The key must be a symmetric encryption key (`SYMMETRIC_DEFAULT`, usage `ENCRYPT_DECRYPT`), enabled, and in the same Region as the proxy. Otherwise the service rejects it, and the Gateway gets a `FailedDeployModel` event with the error.

### Permissions

The service encrypts the configuration under your key with the controller's credentials, so the controller IAM policy needs these two statements:

```json
{
    "Sid": "CustomerKmsDescribe",
    "Effect": "Allow",
    "Action": "kms:DescribeKey",
    "Resource": "*"
},
{
    "Sid": "CustomerKmsCryptoOps",
    "Effect": "Allow",
    "Action": [
        "kms:GenerateDataKey",
        "kms:Decrypt"
    ],
    "Resource": "*",
    "Condition": {
        "StringLike": {
            "kms:EncryptionContext:aws:elasticloadbalancing:proxy-arn": "arn:aws:elasticloadbalancing:*:*:proxy/*"
        }
    }
}
```

- **`CustomerKmsDescribe`** lets the service check that the key is usable before it encrypts with it.
- **`CustomerKmsCryptoOps`** lets the service create and read the data keys that encrypt the configuration. The condition limits both actions to requests whose encryption context names a proxy ARN, which is how the service calls them.

To allow only your own keys, replace `"Resource": "*"` with their ARNs. The key policy must also allow the controller's role to use the key, unless it delegates access to IAM policies.

### Change the key

When you change `customerKmsKeyArn`, the controller sends the proxy's whole configuration again with the new key, and the service re-encrypts it. The proxy isn't replaced, but Envoy restarts with the re-encrypted configuration.

Removing `customerKmsKeyArn` from a proxy that uses your key asks the service to go back to its own key. The service might not support this yet. If it rejects the change, the Gateway gets a `FailedDeployModel` event, and the proxy keeps your key.

### If the service loses access to the key

If you disable the key, schedule it for deletion, or remove the controller's access, the service can't read the proxy's configuration. The proxy's status in the `ELBProxyConfiguration` then shows `ActionRequired`, with the reason from AWS. See [Check the proxy's status](../troubleshooting/index.md#check-the-proxys-status). Restore access to the key to fix it.
