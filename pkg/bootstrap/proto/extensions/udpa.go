// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
//
// Copied from https://github.com/envoyproxy/gateway/blob/v1.10.0/internal/xds/extensions/udpa.go
// because internal packages can't be imported from another module.

package extensions

import udpa "github.com/cncf/xds/go/udpa/type/v1"

var _ = udpa.TypedStruct{}
