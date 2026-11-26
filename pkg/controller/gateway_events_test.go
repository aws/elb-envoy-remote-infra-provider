// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
)

// TestEmitGatewayEvent_MergedKeyFansOutToMembers verifies an event for a merged key (a bare
// GatewayClass name) lands on each member Gateway, not on a same-named Gateway in the EG namespace.
func TestEmitGatewayEvent_MergedKeyFansOutToMembers(t *testing.T) {
	gw := func(ns, name string) *gwapiv1.Gateway {
		return &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	}
	c := infrastore.New()
	c.SetGatewayClass("merged-eg", "merged-eg") // as server.go seeds it on a merged IR push
	c.SetGatewayClass("team-a/gw-a", "merged-eg")
	c.SetGatewayClass("team-b/gw-b", "merged-eg")
	r := newGatewayReconciler(c,
		gw("team-a", "gw-a"), gw("team-b", "gw-b"),
		gw("envoy-gateway-system", "merged-eg"), // same name as the class; must not get the event
	)
	recorder := record.NewFakeRecorder(10)
	r.recorder = recorder

	r.emitGatewayEvent(context.Background(), "merged-eg", corev1.EventTypeNormal,
		GatewayEventReasonWaitingForProxyDeletion, "waiting")

	assert.Len(t, recorder.Events, 2, "one event per member Gateway")

	// A namespace/name key still targets exactly that Gateway.
	r.emitGatewayEvent(context.Background(), "team-a/gw-a", corev1.EventTypeNormal,
		GatewayEventReasonSuccessfullyReconciled, "ok")
	assert.Len(t, recorder.Events, 3)
}
