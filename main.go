// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/controller"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infra"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/inject/sidecar"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/probe"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/server"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/version"
	webhookcore "github.com/aws/elb-envoy-remote-infra-provider/webhooks/core"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/spf13/pflag"

	corev1 "k8s.io/api/core/v1"
	crwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)
	_ = gwapiv1a2.Install(scheme)
	_ = elbproxyv1alpha1.AddToScheme(scheme)
	// Envoy Gateway policies (SecurityPolicy, EnvoyExtensionPolicy): read for the
	// ext_authz and ext_proc backend ports opened on the node security group.
	_ = egv1a1.AddToScheme(scheme)
}

func main() {
	// `probe` subcommand for the sidecar's exec probes; needs no controller config.
	if len(os.Args) > 1 && os.Args[1] == probe.Command {
		os.Exit(probe.Run(os.Args[2:], os.Stderr))
	}

	cfg, err := loadControllerConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	if err := run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config.ControllerConfig) error {
	logger := getLogger(cfg.LogLevel)
	ctrllog.SetLogger(logger.Logger)
	setupLog.Info("starting elb-envoy-remote-infra-provider", "version", version.GitVersion, "commit", version.GitCommit)

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Webhook-only mode: this process exists solely to host the sidecar-injection
	// admission webhook. The webhook must run as a standalone, long-lived process
	if cfg.WebhookConfig.Enable {
		return runWebhookOnly(ctx, cfg, restCfg)
	}

	syncPeriod := cfg.RuntimeConfig.SyncPeriod
	mgrOpts := manager.Options{
		Scheme:                  scheme,
		LeaderElection:          true,
		LeaderElectionID:        "elb-envoy-remote-infra-provider-leader",
		LeaderElectionNamespace: cfg.LeaderElectionNamespace,
		Metrics: metricsserver.Options{
			BindAddress: cfg.MetricsBindAddress,
		},
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
		},
	}
	mgr, err := manager.New(restCfg, mgrOpts)
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	cache := infrastore.New()

	infraManager, err := infra.NewInfra(ctx, cfg.AWSConfig, cfg.ClusterName, mgr.GetClient(), logger, cfg.ExternalManagedTags, cfg.DefaultTags, cache)
	if err != nil {
		return fmt.Errorf("failed to create infra manager: %w", err)
	}

	egNamespace := config.EnvoyGatewayNamespace()
	rec := reconciler.New(logger, infraManager, cache, mgr.GetClient(), egNamespace)

	// Register gateway controller. The returned handles are passed to the gRPC
	// server: routeRec/gatewayRec for cold-cache warm-up, gatewayKeyCh for
	// async reconcile on CreateOrUpdateProxyInfra.
	routeRec, gatewayRec, gatewayKeyCh, err := controller.RegisterGatewayController(ctx, mgr, cache, rec, logger)
	if err != nil {
		return fmt.Errorf("failed to register gateway controller: %w", err)
	}

	grpcServer := server.NewELBProxyServer(cache, rec, routeRec.SyncBackendPorts, gatewayRec.SyncXDSHosts, gatewayKeyCh, logger, server.Config{
		SocketPath: cfg.ServerSocketPath,
		SocketMode: os.FileMode(cfg.ServerSocketMode),
	})
	go func() {
		if err := grpcServer.Start(ctx); err != nil {
			setupLog.Error(err, "gRPC server exited")
		}
	}()

	// Seed known keys from AWS so IR-absent reconciles can fire.
	// Only runs on the leader.
	go func() {
		select {
		case <-mgr.Elected():
		case <-ctx.Done():
			return
		}
		if len(cache.AllKnownKeys()) > 0 {
			setupLog.Info("keys already known from IR, skipping AWS discovery")
		} else {
			keys, err := infraManager.DiscoverManagedKeys(ctx)
			if err != nil {
				setupLog.Error(err, "failed to discover managed keys from AWS")
			} else {
				for _, key := range keys {
					cache.AddKnownKey(key)
				}
				setupLog.Info("seeded known keys from AWS", "count", len(keys))
			}
		}

		// Run the resource garbage collector to clean up orphaned resources
		// leaked by prior pod crashes (lost pendingDeletion state).
		// Runs immediately then every 60 minutes to handle transient API failures.
		resourceGC := infra.NewGarbageCollector(infraManager, cache, mgr.GetClient(), gatewayKeyCh)
		resourceGC.RunPeriodic(ctx, 60*time.Minute)
	}()

	setupLog.Info("starting controller manager")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager exited with error: %w", err)
	}
	return nil
}

// runWebhookOnly runs a minimal manager that serves ONLY the sidecar-injection
// admission webhook.
func runWebhookOnly(ctx context.Context, cfg *config.ControllerConfig, restCfg *rest.Config) error {
	mgr, err := manager.New(restCfg, manager.Options{
		Scheme:                 scheme,
		LeaderElection:         false,
		HealthProbeBindAddress: cfg.WebhookConfig.HealthProbeBindAddress,
		Metrics: metricsserver.Options{
			BindAddress: cfg.MetricsBindAddress,
		},
		WebhookServer: crwebhook.NewServer(crwebhook.Options{
			Port:    cfg.WebhookConfig.BindPort,
			CertDir: cfg.WebhookConfig.CertDir,
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to create webhook manager: %w", err)
	}

	registerSidecarWebhook(mgr, cfg)
	// Ready once the webhook server completes a TLS handshake
	if err := mgr.AddReadyzCheck("ready-webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("failed to add webhook readiness check: %w", err)
	}
	setupLog.Info("running in webhook-only mode", "port", cfg.WebhookConfig.BindPort)

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("webhook manager exited with error: %w", err)
	}
	return nil
}

// registerSidecarWebhook builds the sidecar injector from controller config and
// registers the pod-mutating webhook on the manager's webhook server.
func registerSidecarWebhook(mgr manager.Manager, cfg *config.ControllerConfig) {
	injector := sidecar.NewSidecarInjector(sidecarInjectorConfig(cfg, config.EnvoyGatewayNamespace()))
	webhookcore.NewPodSidecarMutator(injector).SetupWithManager(mgr)
}

// sidecarInjectorConfig maps controller config to the injected sidecar's config.
func sidecarInjectorConfig(cfg *config.ControllerConfig, egNamespace string) sidecar.Config {
	// Only pod-structural env here; all user config (cluster name, region, VPC,
	// tags, ...) reaches the sidecar as flags via --sidecar-extra-args.
	env := []corev1.EnvVar{
		{Name: "ENVOY_GATEWAY_NAMESPACE", Value: egNamespace},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
	}

	return sidecar.Config{
		Image:                     cfg.WebhookConfig.SidecarImage,
		ImagePullPolicy:           corev1.PullPolicy(cfg.WebhookConfig.SidecarImagePullPolicy),
		SocketPath:                cfg.ServerSocketPath,
		EnvoyGatewayContainerName: config.EnvoyGatewayServiceName,
		Env:                       env,
		ExtraArgs:                 cfg.WebhookConfig.SidecarExtraArgs,
		MountCerts:                cfg.WebhookConfig.SidecarMountCerts,
	}
}

func getLogger(level string) logging.Logger {
	logLevel := egv1a1.LogLevelInfo
	switch level {
	case "debug":
		logLevel = egv1a1.LogLevelDebug
	case "error":
		logLevel = egv1a1.LogLevelError
	case "info":
		logLevel = egv1a1.LogLevelInfo
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown log level %q, defaulting to info\n", level)
	}
	return logging.DefaultLogger(os.Stdout, logLevel)
}

func loadControllerConfig() (*config.ControllerConfig, error) {
	cfg := &config.ControllerConfig{}
	fs := pflag.NewFlagSet("elb-envoy-remote-infra-provider", pflag.ExitOnError)
	cfg.BindFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
