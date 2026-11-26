// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
)

func testLogger() logging.Logger {
	return logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo)
}

// fakeQueue is a minimal workqueue for testing.
type fakeQueue[T comparable] struct {
	items []T
}

func (q *fakeQueue[T]) Add(item T) { q.items = append(q.items, item) }

func (q *fakeQueue[T]) Len() int                     { return len(q.items) }
func (q *fakeQueue[T]) Get() (T, bool)               { var z T; return z, false }
func (q *fakeQueue[T]) Done(T)                       {}
func (q *fakeQueue[T]) ShutDown()                    {}
func (q *fakeQueue[T]) ShutDownWithDrain()           {}
func (q *fakeQueue[T]) ShuttingDown() bool           { return false }
func (q *fakeQueue[T]) AddAfter(T, time.Duration)    {}
func (q *fakeQueue[T]) AddRateLimited(T)             {}
func (q *fakeQueue[T]) Forget(T)                     {}
func (q *fakeQueue[T]) NumRequeues(T) int            { return 0 }
func (q *fakeQueue[T]) AddWeight(item T, weight int) {}
func (q *fakeQueue[T]) GetWithPriority() (item T, priority int, shutdown bool) {
	var z T
	return z, 0, false
}
func (q *fakeQueue[T]) DoneWithPriority(item T, priority int) {}

var _ workqueue.TypedRateLimitingInterface[reconcile.Request] = &fakeQueue[reconcile.Request]{}

func makeProxy(name, namespace string) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func makeProxyWithTargetRef(name, namespace, targetName string) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Group: gwapiv1.GroupName,
					Kind:  "Gateway",
					Name:  gwapiv1a2.ObjectName(targetName),
				},
			},
		},
	}
}

// allKeys extracts all NamespacedName keys from queue items. An ELBProxyConfiguration's own item
// shows as "<namespace>/ELBProxyConfiguration:<name>" (see ELBProxyConfigurationRequest).
func allKeys(items []reconcile.Request) []string {
	keys := make([]string, len(items))
	for i, item := range items {
		if item.Namespace != "" {
			keys[i] = item.Namespace + "/" + item.Name
		} else {
			keys[i] = item.Name
		}
	}
	return keys
}

// TestEnqueueRequestForELBProxyConfigurationEvent_Create verifies that Create enqueues
// both the proxy itself (for lifecycle) and the impacted gateway key (for reconciliation).
func TestEnqueueRequestForELBProxyConfigurationEvent_Create(t *testing.T) {
	c := infrastore.New()
	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
	q := &fakeQueue[reconcile.Request]{}
	proxy := makeProxyWithTargetRef("my-proxy", "default", "my-gw")

	h.Create(context.Background(), event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	// Expect: proxy itself + gateway key
	assert.Equal(t, 2, len(q.items))
	assert.ElementsMatch(t, []string{"default/ELBProxyConfiguration:my-proxy", "default/my-gw"}, allKeys(q.items))

	// Verify cache is seeded immediately by the event handler.
	cached, exists := c.GetELBProxyConfiguration("default/my-proxy")
	assert.True(t, exists, "Create should seed the ELBProxyConfiguration cache")
	assert.Equal(t, proxy, cached)
}

// An ELBProxyConfiguration named like the Gateway it targets must still enqueue two distinct items:
// the proxy's own item and the gateway key. With identical items, the queue would merge them and
// the Gateway would be reconciled as if it were the proxy.
func TestEnqueueRequestForELBProxyConfigurationEvent_Create_SameNameAsGateway(t *testing.T) {
	c := infrastore.New()
	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
	q := &fakeQueue[reconcile.Request]{}
	proxy := makeProxyWithTargetRef("my-gw", "default", "my-gw")
	h.Create(context.Background(), event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	require.Len(t, q.items, 2)
	assert.NotEqual(t, q.items[0], q.items[1])
	assert.ElementsMatch(t, []reconcile.Request{
		ELBProxyConfigurationRequest(types.NamespacedName{Namespace: "default", Name: "my-gw"}),
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-gw"}},
	}, q.items)
}

func TestELBProxyConfigurationFromRequest(t *testing.T) {
	nn := types.NamespacedName{Namespace: "default", Name: "my-proxy"}
	got, ok := ELBProxyConfigurationFromRequest(ELBProxyConfigurationRequest(nn))
	assert.True(t, ok)
	assert.Equal(t, nn, got)

	for _, gatewayKey := range []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-proxy"}}, // a Gateway
		{NamespacedName: types.NamespacedName{Name: "merged-class"}},                   // a merged gateway's class
	} {
		_, ok := ELBProxyConfigurationFromRequest(gatewayKey)
		assert.False(t, ok, "gateway key %v must not be read as an ELBProxyConfiguration", gatewayKey)
	}
}

// TestEnqueueRequestForELBProxyConfigurationEvent_Create_NoTargetRef verifies that Create
// with no targetRefs only enqueues the proxy itself (no gateway keys to fan out to).
func TestEnqueueRequestForELBProxyConfigurationEvent_Create_NoTargetRef(t *testing.T) {
	c := infrastore.New()
	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
	q := &fakeQueue[reconcile.Request]{}
	proxy := makeProxy("my-proxy", "default")

	h.Create(context.Background(), event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	assert.Equal(t, 1, len(q.items))
	assert.Equal(t, ELBProxyConfigurationRequest(types.NamespacedName{Namespace: "default", Name: "my-proxy"}), q.items[0])
}

// TestEnqueueRequestForELBProxyConfigurationEvent_Create_ClassScopedTargetRef verifies fan-out
// to all gateways in a GatewayClass plus the proxy itself.
func TestEnqueueRequestForELBProxyConfigurationEvent_Create_ClassScopedTargetRef(t *testing.T) {
	c := infrastore.New()
	c.SetInfra("ns/gw1", nil)
	c.SetGatewayClass("ns/gw1", "my-class")
	c.SetInfra("ns/gw2", nil)
	c.SetGatewayClass("ns/gw2", "my-class")

	proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "my-class"},
			},
		},
	}

	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	// Expect: proxy itself + 2 gateway keys
	assert.Equal(t, 3, len(q.items))
	assert.ElementsMatch(t, []string{"ns/ELBProxyConfiguration:p", "ns/gw1", "ns/gw2"}, allKeys(q.items))
}

// TestEnqueueRequestForELBProxyConfigurationEvent_Create_ClassScopedOutsideControllerNS verifies
// that a class-scoped proxy living OUTSIDE the controller namespace does NOT fan out
// to gateways in that class — only the proxy itself is enqueued.
func TestEnqueueRequestForELBProxyConfigurationEvent_Create_ClassScopedOutsideControllerNS(t *testing.T) {
	c := infrastore.New()
	c.SetInfra("ns/gw1", nil)
	c.SetGatewayClass("ns/gw1", "my-class")

	// Proxy is in "tenant-b" but controller namespace is "tenant-a".
	proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "tenant-b"},
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "my-class"},
			},
		},
	}

	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "tenant-a")
	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	// Only the proxy itself is enqueued — no class-based fan-out.
	assert.Equal(t, 1, len(q.items))
	assert.ElementsMatch(t, []string{"tenant-b/ELBProxyConfiguration:p"}, allKeys(q.items))
}

// TestEnqueueRequestForELBProxyConfigurationEvent_Create_ReverseIndex verifies that
// currently-attached gateways (reverse index) are enqueued alongside the proxy.
func TestEnqueueRequestForELBProxyConfigurationEvent_Create_ReverseIndex(t *testing.T) {
	c := infrastore.New()
	c.SetEffectiveProxy("ns/gw1", "ns/my-proxy")

	proxy := makeProxy("my-proxy", "ns")
	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	// Expect: proxy itself + gateway from reverse index
	assert.Equal(t, 2, len(q.items))
	assert.ElementsMatch(t, []string{"ns/ELBProxyConfiguration:my-proxy", "ns/gw1"}, allKeys(q.items))
}

// TestEnqueueRequestForELBProxyConfigurationEvent_Delete verifies that Delete enqueues
// both the proxy itself and currently-attached gateways.
func TestEnqueueRequestForELBProxyConfigurationEvent_Delete(t *testing.T) {
	c := infrastore.New()
	c.SetEffectiveProxy("default/my-gw", "default/my-proxy")

	h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
	q := &fakeQueue[reconcile.Request]{}
	proxy := makeProxy("my-proxy", "default")

	h.Delete(context.Background(), event.TypedDeleteEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{Object: proxy}, q)

	// Expect: proxy itself + gateway from reverse index
	assert.Equal(t, 2, len(q.items))
	assert.ElementsMatch(t, []string{"default/ELBProxyConfiguration:my-proxy", "default/my-gw"}, allKeys(q.items))
}

func TestEnqueueRequestForELBProxyConfigurationEvent_Update(t *testing.T) {
	tests := []struct {
		name       string
		setupCache func(*infrastore.Cache)
		old        *elbproxyv1alpha1.ELBProxyConfiguration
		new        *elbproxyv1alpha1.ELBProxyConfiguration
		wantItems  int
		wantKeys   []string
	}{
		{
			name:       "no relevant change — still enqueued for drift correction",
			setupCache: func(c *infrastore.Cache) {},
			old:        makeProxy("p", "ns"),
			new:        makeProxy("p", "ns"),
			wantItems:  1,
			wantKeys:   []string{"ns/ELBProxyConfiguration:p"},
		},
		{
			name:       "targetRef added — proxy + new gateway key",
			setupCache: func(c *infrastore.Cache) {},
			old:        makeProxy("p", "ns"),
			new:        makeProxyWithTargetRef("p", "ns", "gw"),
			wantItems:  2,
			wantKeys:   []string{"ns/ELBProxyConfiguration:p", "ns/gw"},
		},
		{
			name: "targetRef removed — proxy + old gateway key (reverse index)",
			setupCache: func(c *infrastore.Cache) {
				c.SetEffectiveProxy("ns/gw", "ns/p")
			},
			old:       makeProxyWithTargetRef("p", "ns", "gw"),
			new:       makeProxy("p", "ns"),
			wantItems: 2,
			wantKeys:  []string{"ns/ELBProxyConfiguration:p", "ns/gw"},
		},
		{
			name:       "only status changed — still enqueued for drift correction",
			setupCache: func(c *infrastore.Cache) {},
			old:        makeProxy("p", "ns"),
			new:        makeProxy("p", "ns"),
			wantItems:  1,
			wantKeys:   []string{"ns/ELBProxyConfiguration:p"},
		},
		{
			name:       "deletionTimestamp set — proxy only (no gateways known)",
			setupCache: func(c *infrastore.Cache) {},
			old:        makeProxy("p", "ns"),
			new: func() *elbproxyv1alpha1.ELBProxyConfiguration {
				p := makeProxy("p", "ns")
				now := metav1.NewTime(time.Now())
				p.DeletionTimestamp = &now
				return p
			}(),
			wantItems: 1,
			wantKeys:  []string{"ns/ELBProxyConfiguration:p"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setupCache(c)
			h := NewEnqueueRequestForELBProxyConfigurationEvent(testLogger(), c, "ns")
			q := &fakeQueue[reconcile.Request]{}
			h.Update(context.Background(), event.TypedUpdateEvent[*elbproxyv1alpha1.ELBProxyConfiguration]{
				ObjectOld: tt.old,
				ObjectNew: tt.new,
			}, q)
			assert.Equal(t, tt.wantItems, len(q.items))
			if len(tt.wantKeys) > 0 {
				assert.ElementsMatch(t, tt.wantKeys, allKeys(q.items))
			}

			// Verify cache is always seeded with the new proxy, regardless of spec change.
			cached, exists := c.GetELBProxyConfiguration("ns/p")
			assert.True(t, exists, "Update should always seed the ELBProxyConfiguration cache")
			assert.Equal(t, tt.new, cached)
		})
	}
}
