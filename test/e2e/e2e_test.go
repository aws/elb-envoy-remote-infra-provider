// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package e2e is the entry point for the ELBProxy gateway extension e2e tests.
// The harness lives in test/e2e/framework, resource builders in
// test/e2e/resources, and test cases in test/e2e/tests.
package e2e

import (
	"flag"
	"testing"

	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/tests"
)

// TestE2E runs each registered test case as a parallel subtest (so -run-test
// and `go test -run TestE2E/<name>` both filter). Cases don't share any
// resources: each one creates its own namespace, EnvoyProxy, and GatewayClass.
func TestE2E(t *testing.T) {
	flag.Parse()

	parent := framework.NewSuite(t)
	framework.Logf(t, "running %d e2e test(s), each with its own namespace, EnvoyProxy, and GatewayClass",
		len(tests.E2ETests))

	for _, tc := range tests.E2ETests {
		tc := tc
		if *framework.FlagRunTest != "" && tc.ShortName != *framework.FlagRunTest {
			continue
		}
		t.Run(tc.ShortName, func(t *testing.T) {
			// Run cases concurrently.
			t.Parallel()
			framework.RunTest(t, parent, tc)
		})
	}
}
