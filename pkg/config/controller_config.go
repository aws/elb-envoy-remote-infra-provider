// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
)

const (
	flagLogLevel                = "log-level"
	flagClusterName             = "cluster-name"
	flagMetricsBindAddress      = "metrics-bind-address"
	flagLeaderElectionNamespace = "leader-election-namespace"
	flagServerSocketPath        = "server-socket-path"
	flagServerSocketMode        = "server-socket-mode"
	flagDefaultTags             = "default-tags"
	flagExternalManagedTags     = "external-managed-tags"

	flagEnableSidecarWebhook   = "enable-sidecar-webhook"
	flagWebhookBindPort        = "webhook-bind-port"
	flagWebhookCertDir         = "webhook-cert-dir"
	flagSidecarImage           = "sidecar-image"
	flagSidecarImagePullPolicy = "sidecar-image-pull-policy"
	flagSidecarMountCerts      = "sidecar-mount-certs"
	flagSidecarExtraArgs       = "sidecar-extra-args"
	flagHealthProbeBindAddr    = "health-probe-bind-addr"
)

// trackingTagKeys are tag keys reserved by the controller.
// Users cannot specify these in --default-tags or --external-managed-tags.
var trackingTagKeys = sets.New(
	tracking.TagKeyCluster,
	tracking.TagKeyStack,
	tracking.TagKeyResourceType,
	tracking.TagKeyManagedBy,
)

// ControllerConfig holds all CLI-configurable settings for the ELBProxy controller.
type ControllerConfig struct {
	// AWSConfig holds AWS cloud client configuration.
	AWSConfig elbproxyaws.CloudConfig
	// RuntimeConfig holds controller-runtime settings (e.g. sync period).
	RuntimeConfig RuntimeConfig
	// LogLevel sets the controller log verbosity (debug, info, error). Defaults to info.
	LogLevel string
	// ClusterName is the name of the Kubernetes cluster. Falls back to CLUSTER_NAME env var.
	ClusterName string
	// MetricsBindAddress is the address the metrics server binds to.
	// Set to :8082 to avoid conflicting with envoy-gateway which uses :8080 when running as a sidecar.
	MetricsBindAddress string
	// LeaderElectionNamespace is the namespace for the leader election lease.
	// Defaults to the namespace the controller pod runs in via POD_NAMESPACE env var.
	LeaderElectionNamespace string
	// ServerSocketPath is the filesystem path of the gRPC Unix domain socket.
	ServerSocketPath string
	// ServerSocketMode is the octal file mode applied to the UDS socket file.
	ServerSocketMode uint32
	// DefaultTags are key-value pairs applied to all proxy resources created by the controller.
	// User-supplied tags on ELBProxyConfiguration take precedence over default tags.
	DefaultTags map[string]string
	// ExternalManagedTags is a list of AWS tag keys that are managed externally.
	// The controller will not add, update, or remove these tag keys during reconciliation.
	ExternalManagedTags []string

	// WebhookConfig holds the sidecar-injection mutating webhook settings.
	WebhookConfig WebhookConfig
}

// WebhookConfig holds settings for the sidecar-injection mutating admission webhook.
type WebhookConfig struct {
	// Enable turns the sidecar-injection webhook on. When false, the controller
	// does not serve the webhook (and the chart should not install the
	// MutatingWebhookConfiguration).
	Enable bool
	// BindPort is the port the webhook server listens on.
	BindPort int
	// CertDir is the directory containing the webhook server's tls.crt and tls.key.
	CertDir string
	// SidecarImage is the fully-qualified image injected as the native sidecar.
	SidecarImage string
	// SidecarImagePullPolicy is the pull policy for the injected sidecar image.
	SidecarImagePullPolicy string
	// SidecarMountCerts controls whether the envoy-gateway certs volume is mounted
	// into the injected sidecar.
	SidecarMountCerts bool
	// SidecarExtraArgs are passed verbatim to the injected sidecar, one arg per entry.
	SidecarExtraArgs []string
	// HealthProbeBindAddress serves the webhook host's /readyz (webhook mode only).
	HealthProbeBindAddress string
}

// BindFlags registers all controller flags.
func (cfg *ControllerConfig) BindFlags(fs *pflag.FlagSet) {
	cfg.AWSConfig.BindFlags(fs)
	cfg.RuntimeConfig.BindFlags(fs)
	fs.StringVar(&cfg.LogLevel, flagLogLevel, "info", "Log level (debug, info, error)")
	fs.StringVar(&cfg.ClusterName, flagClusterName, os.Getenv("CLUSTER_NAME"), "Kubernetes cluster name (overrides CLUSTER_NAME env var)")
	fs.StringVar(&cfg.MetricsBindAddress, flagMetricsBindAddress, ":8082", "Address the metrics server binds to")
	fs.StringVar(&cfg.LeaderElectionNamespace, flagLeaderElectionNamespace, os.Getenv("POD_NAMESPACE"), "Namespace for the leader election lease (defaults to POD_NAMESPACE env var)")
	fs.StringVar(&cfg.ServerSocketPath, flagServerSocketPath, "/var/run/elb-envoy-remote-infra-provider/server.sock", "Filesystem path of the gRPC Unix domain socket the server listens on")
	fs.Uint32Var(&cfg.ServerSocketMode, flagServerSocketMode, 0o660, "File mode (octal) applied to the UDS socket file")
	fs.StringToStringVar(&cfg.DefaultTags, flagDefaultTags, nil, "Default AWS tags applied to all proxy resources (key=value pairs)")
	fs.StringSliceVar(&cfg.ExternalManagedTags, flagExternalManagedTags, nil, "AWS tag keys managed externally; the controller will not add, update, or remove these keys")

	fs.BoolVar(&cfg.WebhookConfig.Enable, flagEnableSidecarWebhook, false, "Enable the mutating admission webhook that injects the elb-envoy-remote-infra-provider sidecar into the envoy-gateway pod")
	fs.IntVar(&cfg.WebhookConfig.BindPort, flagWebhookBindPort, 9443, "Port the webhook server binds to")
	fs.StringVar(&cfg.WebhookConfig.CertDir, flagWebhookCertDir, "/tmp/k8s-webhook-server/serving-certs", "Directory holding the webhook server TLS certificate (tls.crt) and key (tls.key)")
	fs.StringVar(&cfg.WebhookConfig.SidecarImage, flagSidecarImage, "", "Fully-qualified image injected as the elb-envoy-remote-infra-provider native sidecar (required when the webhook is enabled)")
	fs.StringVar(&cfg.WebhookConfig.SidecarImagePullPolicy, flagSidecarImagePullPolicy, "IfNotPresent", "Image pull policy for the injected sidecar (Always, IfNotPresent, Never)")
	fs.BoolVar(&cfg.WebhookConfig.SidecarMountCerts, flagSidecarMountCerts, true, "Mount the envoy-gateway certs volume into the injected sidecar")
	fs.StringArrayVar(&cfg.WebhookConfig.SidecarExtraArgs, flagSidecarExtraArgs, nil, "Argument passed to the injected sidecar (repeatable, one argument per flag)")
	fs.StringVar(&cfg.WebhookConfig.HealthProbeBindAddress, flagHealthProbeBindAddr, ":61779", "Address the webhook host serves /readyz on (webhook mode only)")
}

// Validate checks the controller configuration for invalid combinations.
func (cfg *ControllerConfig) Validate() error {
	if err := cfg.validateDefaultTagsCollisionWithTrackingTags(); err != nil {
		return err
	}
	if err := cfg.validateExternalManagedTagsCollisionWithTrackingTags(); err != nil {
		return err
	}
	if err := cfg.validateExternalManagedTagsCollisionWithDefaultTags(); err != nil {
		return err
	}
	if err := cfg.validateWebhookConfig(); err != nil {
		return err
	}
	return nil
}

func (cfg *ControllerConfig) validateWebhookConfig() error {
	if cfg.WebhookConfig.Enable && cfg.WebhookConfig.SidecarImage == "" {
		return fmt.Errorf("--%s is required when --%s is set", flagSidecarImage, flagEnableSidecarWebhook)
	}
	return nil
}

func (cfg *ControllerConfig) validateDefaultTagsCollisionWithTrackingTags() error {
	for k := range cfg.DefaultTags {
		if trackingTagKeys.Has(k) {
			return fmt.Errorf("tag key %q cannot be specified in --%s (reserved by controller)", k, flagDefaultTags)
		}
	}
	return nil
}

func (cfg *ControllerConfig) validateExternalManagedTagsCollisionWithTrackingTags() error {
	for _, k := range cfg.ExternalManagedTags {
		if trackingTagKeys.Has(k) {
			return fmt.Errorf("tag key %q cannot be specified in --%s (reserved by controller)", k, flagExternalManagedTags)
		}
	}
	return nil
}

func (cfg *ControllerConfig) validateExternalManagedTagsCollisionWithDefaultTags() error {
	for _, k := range cfg.ExternalManagedTags {
		if _, ok := cfg.DefaultTags[k]; ok {
			return fmt.Errorf("tag key %q cannot be specified in both --%s and --%s", k, flagDefaultTags, flagExternalManagedTags)
		}
	}
	return nil
}
