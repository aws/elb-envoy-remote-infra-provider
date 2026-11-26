// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// NewRequeueNeededAfter constructs a new RequeueNeededAfter to instruct
// controller-runtime to requeue the processing item after the specified duration
// without it being logged as an error.
func NewRequeueNeededAfter(reason string, duration time.Duration) *RequeueNeededAfter {
	return &RequeueNeededAfter{
		reason:   reason,
		duration: duration,
	}
}

// RequeueNeededAfter is an error that instructs controller-runtime to requeue the
// processing item after the specified duration without it being logged as an error.
// Use it when an error condition is expected and can be resolved by a retry, for
// example when a dependency has not been fulfilled yet but is expected to be
// fulfilled after the given duration.
type RequeueNeededAfter struct {
	reason   string
	duration time.Duration
}

func (e *RequeueNeededAfter) Reason() string {
	return e.reason
}

func (e *RequeueNeededAfter) Duration() time.Duration {
	return e.duration
}

func (e *RequeueNeededAfter) Error() string {
	return fmt.Sprintf("requeue needed after %v: %v", e.duration, e.reason)
}

// HandleReconcileError handles errors from reconcile handlers, respecting
// runtime requeue errors.
//   - Converts RequeueNeededAfter → Result{RequeueAfter}, nil
//   - Returns all other errors to controller-runtime as failures
func HandleReconcileError(inputErr error, log logging.Logger) (reconcile.Result, error) {
	if inputErr == nil {
		return reconcile.Result{}, nil
	}
	var requeueNeededAfter *RequeueNeededAfter
	if errors.As(inputErr, &requeueNeededAfter) {
		log.V(1).Info("requeue after", "duration", requeueNeededAfter.Duration(), "reason", requeueNeededAfter.Reason())
		return reconcile.Result{RequeueAfter: requeueNeededAfter.Duration()}, nil
	}
	return reconcile.Result{}, inputErr
}

// RequeueReasonPreviousProxyDeleting is the requeue reason used while the previous
// proxy for a gateway is still being deleted; the controller surfaces it as an event.
const RequeueReasonPreviousProxyDeleting = "previous proxy is still being deleted"

// RequeueReasonProxyProvisioning: the proxy is still being created.
const RequeueReasonProxyProvisioning = "proxy provisioning is still in progress"

// RequeueReasonWaitingForProxyDeletion is the requeue reason used during teardown while the
// gateway's proxy is still being deleted, so security group cleanup is held until it is gone.
const RequeueReasonWaitingForProxyDeletion = "waiting for proxy deletion before deleting security groups"

// IsRequeueAfterReason reports whether err is a RequeueNeededAfter with the given reason.
func IsRequeueAfterReason(err error, reason string) bool {
	var requeueNeededAfter *RequeueNeededAfter
	return errors.As(err, &requeueNeededAfter) && requeueNeededAfter.Reason() == reason
}

// IsRequeueError returns true when err indicates a requeue (expected retry).
func IsRequeueError(err error) bool {
	var requeueNeededAfter *RequeueNeededAfter
	return errors.As(err, &requeueNeededAfter)
}
