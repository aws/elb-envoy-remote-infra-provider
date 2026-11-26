// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"google.golang.org/grpc"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/controller"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

const (
	ELBProxyGRPCServer = "elbproxy-grpc-server"

	// defaultUnixSocketMode is the default file mode applied to the UDS socket file.
	// 0660 grants read/write to the owner and the owning group, which is typical
	// for sidecar communication where both processes share a group identity.
	defaultUnixSocketMode os.FileMode = 0o660
)

// Config configures the gRPC Unix domain socket listener.
type Config struct {
	// SocketPath is the filesystem path of the Unix domain socket to listen on.
	SocketPath string
	// SocketMode is the file permission applied to the socket file.
	// If zero, defaults to 0660.
	SocketMode os.FileMode
}

// ELBProxyServer implements a remote infrastructure provider used by Envoy Gateway.
// It receives InfraIR from Envoy Gateway, stores it in the cache, and drives reconciliation.
//
// CreateOrUpdateProxyInfra is asynchronous: it updates the cache and enqueues the
// gateway key into the controller queue, then returns immediately to Envoy Gateway.
// The controller reconciles the key in the background, calling the ELBProxy API without
// blocking the gRPC call.
//
// DeleteProxyInfra is also asynchronous: it marks the key as pendingDeletion and
// enqueues it into the controller queue. The controller detects the pending state,
// runs teardown (DeleteProxy + SG delete), and requeues with backoff on failure.
type ELBProxyServer struct {
	pb.UnimplementedEnvoyGatewayRemoteInfrastructureProviderServer
	cache            *infrastore.Cache
	reconciler       *reconciler.Reconciler
	syncBackendPorts func(ctx context.Context, infra *ir.Infra) error
	syncXDSHosts     func(ctx context.Context) error
	gatewayKeyCh     controller.GatewayEventCh
	logger           logging.Logger
	cfg              Config
}

func NewELBProxyServer(
	cache *infrastore.Cache,
	rec *reconciler.Reconciler,
	syncBackendPorts func(ctx context.Context, infra *ir.Infra) error,
	syncXDSHosts func(ctx context.Context) error,
	gatewayKeyCh controller.GatewayEventCh,
	logger logging.Logger,
	cfg Config,
) *ELBProxyServer {
	if cfg.SocketMode == 0 {
		cfg.SocketMode = defaultUnixSocketMode
	}
	return &ELBProxyServer{
		cache:            cache,
		reconciler:       rec,
		syncBackendPorts: syncBackendPorts,
		syncXDSHosts:     syncXDSHosts,
		gatewayKeyCh:     gatewayKeyCh,
		logger:           logger.WithName(ELBProxyGRPCServer),
		cfg:              cfg,
	}
}

func (s *ELBProxyServer) Start(ctx context.Context) error {
	lis, err := s.listen()
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer()
	pb.RegisterEnvoyGatewayRemoteInfrastructureProviderServer(grpcServer, s)
	s.logger.Info("gRPC server listening", "socket", s.cfg.SocketPath)

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("gRPC server failed: %w", err)
	}
	return nil
}

// listen creates a Unix domain socket listener at the configured path.
// It ensures the parent directory exists, removes any stale socket file
// from a previous run, and applies the configured file permission.
func (s *ELBProxyServer) listen() (net.Listener, error) {
	if s.cfg.SocketPath == "" {
		return nil, errors.New("socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}
	// Remove any stale socket left from a previous process. Ignore not-exist errors.
	if err := os.Remove(s.cfg.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to remove stale socket %s: %w", s.cfg.SocketPath, err)
	}
	lis, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on unix %s: %w", s.cfg.SocketPath, err)
	}
	if err := os.Chmod(s.cfg.SocketPath, s.cfg.SocketMode); err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("failed to chmod socket %s: %w", s.cfg.SocketPath, err)
	}
	return lis, nil
}

func (s *ELBProxyServer) CreateOrUpdateProxyInfra(ctx context.Context, req *pb.CreateOrUpdateProxyInfraRequest) (*pb.CreateOrUpdateProxyInfraResponse, error) {
	if req.Infra == nil || req.Infra.Proxy == nil {
		return nil, fmt.Errorf("IR missing proxy field")
	}

	key := req.Infra.Proxy.Name
	infra := ir.New(req.Infra)

	// Cold-cache warm-up: on first infra arrival after pod restart, seed backend
	// ports (routes don't fire change events for pre-existing routes) and xDS hosts
	// (EndpointSlice Update may have fired before the cache was ready).
	if _, exists := s.cache.GetInfra(key); !exists {
		if err := s.syncBackendPorts(ctx, infra); err != nil {
			s.logger.Error(err, "SyncBackendPorts warm-up failed", "key", key)
		}
		if err := s.syncXDSHosts(ctx); err != nil {
			s.logger.Error(err, "SyncXDSHosts warm-up failed", "key", key)
		}
	}

	s.cache.SetInfra(key, infra)

	// For merged gateways, seed the GatewayClass cache entry so cert/endpoint-triggered reconciles can resolve the ELBProxyConfiguration.
	// gatewayClassFor checks GetGatewayClass(key) and skips if missing.
	if elbproxyutil.IsMergedGatewayProxyInfra(infra) {
		s.cache.SetGatewayClass(key, key)
	}

	// Enqueue the key into the controller queue and return immediately.
	// The controller reconciles asynchronously, calling the ELBProxy API in the
	// background. This prevents Envoy Gateway from blocking on AWS API latency.
	controller.EnqueueGatewayKey(s.gatewayKeyCh, key)

	return &pb.CreateOrUpdateProxyInfraResponse{}, nil
}

func (s *ELBProxyServer) DeleteProxyInfra(ctx context.Context, req *pb.DeleteProxyInfraRequest) (*pb.DeleteProxyInfraResponse, error) {
	if req.Infra == nil || req.Infra.Proxy == nil {
		return nil, fmt.Errorf("IR missing proxy field")
	}

	key := req.Infra.Proxy.Name

	// Mark for deletion — clears IR/ports/known but keeps the key tracked in
	// pendingDeletion so the controller queue can drive teardown with retry.
	s.cache.MarkPendingDeletion(key)

	s.logger.Info("reconciling node SG rules before delete", "key", key, "remainingPorts", s.cache.AllBackendPorts())
	if err := s.reconciler.ReconcileNodeSGRules(ctx); err != nil {
		s.logger.Error(err, "failed to reconcile node SG rules before delete", "key", key)
	}

	// Enqueue into the controller work queue. ReconcileKey detects pendingDeletion
	// and runs the teardown. On failure, controller-runtime requeues with backoff.
	s.logger.Info("enqueuing delete into controller queue", "key", key)
	controller.EnqueueGatewayKey(s.gatewayKeyCh, key)

	return &pb.DeleteProxyInfraResponse{}, nil
}

func (s *ELBProxyServer) CreateOrUpdateRateLimitInfra(ctx context.Context, req *pb.CreateOrUpdateRateLimitInfraRequest) (*pb.CreateOrUpdateRateLimitInfraResponse, error) {
	s.logger.Info("CreateOrUpdateRateLimitInfra called")
	return &pb.CreateOrUpdateRateLimitInfraResponse{}, nil
}

func (s *ELBProxyServer) DeleteRateLimitInfra(ctx context.Context, req *pb.DeleteRateLimitInfraRequest) (*pb.DeleteRateLimitInfraResponse, error) {
	s.logger.Info("DeleteRateLimitInfra called")
	return &pb.DeleteRateLimitInfraResponse{}, nil
}
