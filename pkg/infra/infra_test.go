// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ktypes "k8s.io/apimachinery/pkg/types"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
)

type fakeStackDeployer struct{ err error }

func (f *fakeStackDeployer) Deploy(_ context.Context, _ core.Stack) (*deploy.DeployResult, error) {
	return nil, f.err
}

type fakeBackendSGProvider struct {
	releaseCalls int
	// id is the backend SG ID Get returns.
	id string
}

func (f *fakeBackendSGProvider) Get(_ context.Context, _ networking.ResourceType, _ []ktypes.NamespacedName) (string, error) {
	return f.id, nil
}

func (f *fakeBackendSGProvider) Release(_ context.Context, _ networking.ResourceType, _ []ktypes.NamespacedName) error {
	f.releaseCalls++
	return nil
}

// TestDeleteProxyInfra_HoldsBackendSGWhileProxyDeleting verifies teardown returns the wait
// requeue without releasing the backend SG while the proxy is still being deleted, and
// releases it once the empty stack deploys cleanly.
func TestDeleteProxyInfra_HoldsBackendSGWhileProxyDeleting(t *testing.T) {
	infraIR := ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Name: "ns/gw"}})

	tests := []struct {
		name         string
		deployErr    error
		wantWaitErr  bool
		wantErr      bool
		wantReleases int
	}{
		{
			name:         "proxy still deleting holds backend SG",
			deployErr:    reconciler.NewRequeueNeededAfter(reconciler.RequeueReasonWaitingForProxyDeletion, 30*time.Second),
			wantWaitErr:  true,
			wantErr:      true,
			wantReleases: 0,
		},
		{
			name:         "teardown complete releases backend SG",
			wantReleases: 1,
		},
		{
			name:         "other deploy error still releases backend SG",
			deployErr:    errors.New("DependencyViolation"),
			wantErr:      true,
			wantReleases: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sgProvider := &fakeBackendSGProvider{}
			i := &Infra{
				logger:            newTestGCLogger(),
				stackDeployer:     &fakeStackDeployer{err: tt.deployErr},
				backendSGProvider: sgProvider,
			}

			err := i.DeleteProxyInfra(context.Background(), infraIR)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantWaitErr,
				reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonWaitingForProxyDeletion))
			assert.Equal(t, tt.wantReleases, sgProvider.releaseCalls)
		})
	}
}

// After a gateway deletion the node SG rules are recomputed here; the Envoy Gateway xDS
// and Wasm module server ports must stay open for the remaining proxies.
func TestReconcileNodeSGRules_OpensControlPlanePorts(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := networking.NewMockNodeSGResolver(ctrl)
	resolver.EXPECT().FindNodeSGsByClusterTag(gomock.Any()).Return([]string{"sg-node"}, nil)

	var got []string
	sgReconciler := networking.NewMockSecurityGroupReconciler(ctrl)
	sgReconciler.EXPECT().ReconcileIngress(gomock.Any(), "sg-node", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, perms []networking.IPPermissionInfo, _ ...networking.SecurityGroupReconcileOption) error {
			for _, p := range perms {
				got = append(got, fmt.Sprintf("%s/%d from %s", *p.Permission.IpProtocol, *p.Permission.FromPort,
					*p.Permission.UserIdGroupPairs[0].GroupId))
			}
			return nil
		})

	i := &Infra{
		logger:              newTestGCLogger(),
		backendSGProvider:   &fakeBackendSGProvider{id: "sg-backend"},
		nodeSGResolver:      resolver,
		sgReconciler:        sgReconciler,
		controllerNamespace: "envoy-gateway-system",
	}
	require.NoError(t, i.ReconcileNodeSGRules(context.Background(), []ir.PortProtocol{{Port: 8080, Protocol: "tcp"}}, true))
	assert.Equal(t, []string{"tcp/8080 from sg-backend", "tcp/18000 from sg-backend", "tcp/18002 from sg-backend"}, got)
}
