// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
)

// ELBProxyServer implements a remote infrastructure provider used by Envoy Gateway.
type ELBProxyServer struct {
	pb.UnimplementedEnvoyGatewayRemoteInfrastructureProviderServer
}
