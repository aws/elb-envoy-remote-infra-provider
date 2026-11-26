## Summary

<!-- What does this PR change, and why? Link related issues with "Fixes #<number>". -->

## Testing

<!--
CI runs go vet (including test/e2e), staticcheck, the unit tests, govulncheck,
and a check that generated files are committed. It can't run the e2e tests,
because they need AWS. List the unit tests you added and the e2e tests or
cluster checks you ran (see test/e2e/README.md).
-->

## Checklist

- [ ] Added or updated unit tests
- [ ] Ran the related e2e tests, or tested on a cluster, if behavior changed
- [ ] If `pkg/api` types changed: ran `make generate manifests api-ref-docs` and committed the result (`make manifests` also copies the CRD into the Helm chart)
