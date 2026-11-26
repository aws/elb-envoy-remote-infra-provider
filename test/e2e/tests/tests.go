// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package tests holds the e2e test cases. Each file registers into
// E2ETests via init().
package tests

import (
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
)

// E2ETests is the registry consumed by TestE2E.
var E2ETests []framework.TestCase
