// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package util

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Policy condition types and reasons used on ELBProxyConfiguration.Status.Conditions.
// https://gateway-api.sigs.k8s.io/geps/gep-713/
const (
	// ELBProxyConfigurationAcceptedConditionType signals whether the ELBProxyConfiguration is
	// currently applied to at least one Gateway (True) or rejected because
	// another ELBProxyConfiguration beats it in tiebreak (False, Conflicted).
	ELBProxyConfigurationAcceptedConditionType = string(gwapiv1.PolicyConditionAccepted)

	// ELBProxyConfigurationReasonAccepted is set on Accepted=True when the ELBProxyConfiguration
	// is currently the winning attachment for at least one target.
	ELBProxyConfigurationReasonAccepted = string(gwapiv1.PolicyReasonAccepted)

	// ELBProxyConfigurationReasonConflicted is set on Accepted=False when another
	// ELBProxyConfiguration targeting the same resource outranks this one in tiebreak.
	ELBProxyConfigurationReasonConflicted = string(gwapiv1.PolicyReasonConflicted)

	// ELBProxyConfigurationReasonInvalidTargetRef is set on Accepted=False when the
	// ELBProxyConfiguration has a GatewayClass-scoped targetRef but does not live in
	// the controller namespace. GatewayClass-scoped attachments are only
	// honored from the controller's own namespace; refs from other namespaces
	// are silently ignored during resolution, so the proxy can never take effect.
	ELBProxyConfigurationReasonInvalidTargetRef = "InvalidTargetRef"
)

// Programmed condition and reasons: the AWS proxy's status on ELBProxyConfiguration.
const (
	// ELBProxyConfigurationProgrammedConditionType is True when the AWS proxy is active.
	ELBProxyConfigurationProgrammedConditionType = "Programmed"

	// ELBProxyConfigurationReasonProgrammed is set on Programmed=True.
	ELBProxyConfigurationReasonProgrammed = "Programmed"

	// ELBProxyConfigurationReasonPending is set while the proxy is being created.
	ELBProxyConfigurationReasonPending = "Pending"

	// ELBProxyConfigurationReasonActionRequired is set when the proxy requires action;
	// the AWS status reasons are in the message.
	ELBProxyConfigurationReasonActionRequired = "ActionRequired"

	// ELBProxyConfigurationReasonCreateFailed is set when the proxy failed to create.
	ELBProxyConfigurationReasonCreateFailed = "CreateFailed"
)

const (
	// ELBProxyConfigurationReasonDeleting is set while the proxy is being deleted, on
	// Gateway deletion or replacement.
	ELBProxyConfigurationReasonDeleting = "Deleting"
)
