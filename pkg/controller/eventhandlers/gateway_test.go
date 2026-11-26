// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestEnqueueRequestsForGatewayEvent_Update(t *testing.T) {
	old := labeledGateway("default", "gw", map[string]string{"env": "dev"})
	tests := []struct {
		name string
		new  *gwapiv1.Gateway
		want []reconcile.Request
	}{
		{
			name: "labels changed",
			new:  labeledGateway("default", "gw", map[string]string{"env": "prod"}),
			want: []reconcile.Request{gatewayRequest("default", "gw")},
		},
		{
			name: "labels unchanged",
			new:  labeledGateway("default", "gw", map[string]string{"env": "dev"}),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewEnqueueRequestsForGatewayEvent(testLogger())
			q := &fakeQueue[reconcile.Request]{}
			h.Update(context.Background(), event.TypedUpdateEvent[*gwapiv1.Gateway]{ObjectOld: old, ObjectNew: tt.new}, q)
			assert.ElementsMatch(t, tt.want, q.items)
		})
	}
}
