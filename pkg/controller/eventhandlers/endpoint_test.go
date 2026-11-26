// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

func makeCache(keys ...string) *infrastore.Cache {
	c := infrastore.New()
	for _, k := range keys {
		c.SetInfra(k, ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Metadata: &pb.InfraMetadata{}}}))
	}
	return c
}

// xdsSlice builds an EndpointSlice of the Envoy Gateway Service with ready addrs.
func xdsSlice(name string, readyAddrs ...string) *discoveryv1.EndpointSlice {
	return endpointSlice(config.EnvoyGatewayNamespace(), name, config.EnvoyGatewayServiceName, readyAddrs...)
}

func endpointSlice(namespace, name, service string, readyAddrs ...string) *discoveryv1.EndpointSlice {
	eps := make([]discoveryv1.Endpoint, 0, len(readyAddrs))
	for _, addr := range readyAddrs {
		eps = append(eps, discoveryv1.Endpoint{
			Addresses:  []string{addr},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
		})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   eps,
	}
}

func endpointTestClient(slices ...*discoveryv1.EndpointSlice) client.Client {
	s := runtime.NewScheme()
	_ = discoveryv1.AddToScheme(s)
	b := fake.NewClientBuilder().WithScheme(s)
	for _, slice := range slices {
		b = b.WithObjects(slice)
	}
	return b.Build()
}

func TestListXDSHosts(t *testing.T) {
	c := endpointTestClient(
		xdsSlice("eg-a", "10.0.0.2", "10.0.0.1"),
		xdsSlice("eg-b", "10.0.0.3", "10.0.0.1"),
		endpointSlice(config.EnvoyGatewayNamespace(), "other-svc", "other", "10.0.0.9"),
		endpointSlice("other-ns", "eg-elsewhere", config.EnvoyGatewayServiceName, "10.0.0.8"),
	)
	hosts, err := ListXDSHosts(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, hosts,
		"all slices of the Envoy Gateway Service, deduplicated and sorted, and nothing else")
}

// The slices in the client are what the informer cache holds after the event.
func TestEnqueueRequestsForEndpointEvent(t *testing.T) {
	sliceA := xdsSlice("eg-a", "10.0.0.1")
	sliceB := xdsSlice("eg-b", "10.0.0.2")
	tests := []struct {
		name      string
		slices    []*discoveryv1.EndpointSlice
		cached    []string // xDS hosts in the cache before the event
		send      func(h *EnqueueRequestsForEndpointEvent, q *fakeQueue[reconcile.Request])
		wantHosts []string
		wantItems int
	}{
		{
			name:   "create of a second slice keeps the first slice's hosts",
			slices: []*discoveryv1.EndpointSlice{sliceA, sliceB},
			cached: []string{"10.0.0.1"},
			send: func(h *EnqueueRequestsForEndpointEvent, q *fakeQueue[reconcile.Request]) {
				h.Create(context.Background(), event.TypedCreateEvent[*discoveryv1.EndpointSlice]{Object: sliceB}, q)
			},
			wantHosts: []string{"10.0.0.1", "10.0.0.2"},
			wantItems: 2,
		},
		{
			name:   "update of one slice keeps the other slice's hosts",
			slices: []*discoveryv1.EndpointSlice{xdsSlice("eg-a", "10.0.0.5"), sliceB},
			cached: []string{"10.0.0.1", "10.0.0.2"},
			send: func(h *EnqueueRequestsForEndpointEvent, q *fakeQueue[reconcile.Request]) {
				h.Update(context.Background(), event.TypedUpdateEvent[*discoveryv1.EndpointSlice]{
					ObjectOld: sliceA, ObjectNew: xdsSlice("eg-a", "10.0.0.5"),
				}, q)
			},
			wantHosts: []string{"10.0.0.2", "10.0.0.5"},
			wantItems: 2,
		},
		{
			name:   "delete of a slice leaves the remaining slice's hosts",
			slices: []*discoveryv1.EndpointSlice{sliceB},
			cached: []string{"10.0.0.1", "10.0.0.2"},
			send: func(h *EnqueueRequestsForEndpointEvent, q *fakeQueue[reconcile.Request]) {
				h.Delete(context.Background(), event.TypedDeleteEvent[*discoveryv1.EndpointSlice]{Object: sliceA}, q)
			},
			wantHosts: []string{"10.0.0.2"},
			wantItems: 2,
		},
		{
			name:   "unchanged combined hosts are not enqueued",
			slices: []*discoveryv1.EndpointSlice{sliceA, sliceB},
			cached: []string{"10.0.0.1", "10.0.0.2"},
			send: func(h *EnqueueRequestsForEndpointEvent, q *fakeQueue[reconcile.Request]) {
				h.Update(context.Background(), event.TypedUpdateEvent[*discoveryv1.EndpointSlice]{ObjectOld: sliceA, ObjectNew: sliceA}, q)
			},
			wantHosts: []string{"10.0.0.1", "10.0.0.2"},
			wantItems: 0,
		},
		{
			name:   "no ready address keeps the last known hosts",
			slices: nil,
			cached: []string{"10.0.0.1"},
			send: func(h *EnqueueRequestsForEndpointEvent, q *fakeQueue[reconcile.Request]) {
				h.Delete(context.Background(), event.TypedDeleteEvent[*discoveryv1.EndpointSlice]{Object: sliceA}, q)
			},
			wantHosts: []string{"10.0.0.1"},
			wantItems: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := makeCache("default/gw1", "default/gw2")
			c.SetXDSHosts(tt.cached)
			h := NewEnqueueRequestsForEndpointEvent(testLogger(), endpointTestClient(tt.slices...), c)
			q := &fakeQueue[reconcile.Request]{}
			tt.send(h, q)
			assert.Equal(t, tt.wantHosts, c.XDSHosts())
			assert.Equal(t, tt.wantItems, len(q.items))
		})
	}
}

// TestEnqueueRequestsForEndpointEvent_KnownKeyWithoutIR verifies that a key known
// only via tag discovery (no IR, the post-restart state) still gets a reconcile:
// xDS endpoints live in the IR-free archive.
func TestEnqueueRequestsForEndpointEvent_KnownKeyWithoutIR(t *testing.T) {
	c := infrastore.New()
	c.AddKnownKey("default/gw-no-ir")
	slice := xdsSlice("eg-a", "10.0.0.1")
	h := NewEnqueueRequestsForEndpointEvent(testLogger(), endpointTestClient(slice), c)
	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[*discoveryv1.EndpointSlice]{Object: slice}, q)
	assert.Equal(t, 1, len(q.items), "key known without IR must still be enqueued")
}

func TestReadyAddresses(t *testing.T) {
	tests := []struct {
		name  string
		slice *discoveryv1.EndpointSlice
		want  []string
	}{
		{
			name:  "empty slice",
			slice: &discoveryv1.EndpointSlice{},
			want:  nil,
		},
		{
			name: "ready endpoint",
			slice: &discoveryv1.EndpointSlice{Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
			}},
			want: []string{"10.0.0.1"},
		},
		{
			name: "not-ready skipped",
			slice: &discoveryv1.EndpointSlice{Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(false)}},
			}},
			want: nil,
		},
		{
			name: "nil ready condition skipped",
			slice: &discoveryv1.EndpointSlice{Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: nil}},
			}},
			want: nil,
		},
		{
			name: "multiple addresses — sorted",
			slice: &discoveryv1.EndpointSlice{Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{"10.0.0.9"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
				{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
			}},
			want: []string{"10.0.0.1", "10.0.0.9"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, readyAddresses(tt.slice))
		})
	}
}
