// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

// Gateway events emitted by the ELBProxy Gateway extension.
const (
	// GatewayEventReasonFailedDeployModel is emitted when the extension fails to
	// build or deploy the proxy model to AWS (e.g. ELBProxy API error, subnet
	// resolution failure, throttling).
	GatewayEventReasonFailedDeployModel = "FailedDeployModel"

	// GatewayEventReasonSuccessfullyReconciled is emitted when a gateway is
	// successfully reconciled and the proxy infrastructure is up to date.
	GatewayEventReasonSuccessfullyReconciled = "SuccessfullyReconciled"

	// GatewayEventReasonWaitingForProxyDeletion is emitted while the gateway's previous
	// proxy is still being deleted; a new proxy is created once it is gone.
	GatewayEventReasonWaitingForProxyDeletion = "WaitingForProxyDeletion"
)
