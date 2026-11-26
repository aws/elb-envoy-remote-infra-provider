// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
)

// TestUpdate_TerminatingServiceIsFinishedAndRequeued verifies a still-desired Service stuck
// terminating on our finalizer (e.g. teardown deleted it, then the gateway was re-applied)
// has its finalizer removed and the reconcile requeued, so the next pass recreates it.
func TestUpdate_TerminatingServiceIsFinishedAndRequeued(t *testing.T) {
	now := metav1.Now()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "envoy-gateway-system",
			Name:              "elbp-default-gw",
			Finalizers:        []string{ServiceFinalizer},
			DeletionTimestamp: &now,
		},
	}
	k8sClient := fake.NewClientBuilder().WithObjects(svc).Build()
	m := NewDefaultServiceManager(k8sClient, tracking.NewProvider("test-cluster"),
		logging.Logger{Logger: logr.New(&log.NullLogSink{})})

	live := &corev1.Service{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, live))

	_, err := m.Update(context.Background(), nil, live)
	require.True(t, reconciler.IsRequeueError(err), "expected a requeue to recreate the Service, got %v", err)

	// With the finalizer gone the API server reaps the Service.
	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, &corev1.Service{})
	assert.True(t, apierrors.IsNotFound(err), "terminating Service should be reaped, got %v", err)
}
