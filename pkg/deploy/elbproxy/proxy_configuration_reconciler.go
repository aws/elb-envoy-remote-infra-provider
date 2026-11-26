// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"crypto/sha256"
	"fmt"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
)

// ProxyConfigurationReconciler reconciles proxy configuration (bootstrap + archive)
// against the live hashes from ConfigurationMetadata in GetProxy
//
// BootstrapFile is only included when changed — it triggers an Envoy restart.
// ArchiveBytes is included when changed — no restart.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination proxy_configuration_reconciler_mock.go -package elbproxy github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy ProxyConfigurationReconciler
type ProxyConfigurationReconciler interface {
	Reconcile(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) error
}

// NewDefaultProxyConfigurationReconciler constructs a new defaultProxyConfigurationReconciler.
func NewDefaultProxyConfigurationReconciler(client elbproxyaws.ELBProxyClient, logger logging.Logger) *defaultProxyConfigurationReconciler {
	return &defaultProxyConfigurationReconciler{
		client: client,
		logger: logger,
	}
}

var _ ProxyConfigurationReconciler = &defaultProxyConfigurationReconciler{}

type defaultProxyConfigurationReconciler struct {
	client elbproxyaws.ELBProxyClient
	logger logging.Logger
}

func (r *defaultProxyConfigurationReconciler) Reconcile(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) error {
	arn := sdkProxy.ProxyDetails.Arn
	desired := resProxy.Configuration

	desiredBootstrapHash := hashBytes(desired.BootstrapFile)
	desiredArchiveHash := hashBytes(desired.ArchiveBytes)

	meta := sdkProxy.ProxyDetails.ConfigurationMetadata

	var input elbproxyaws.UpdateProxyConfigurationInput

	// Only include BootstrapFile if changed — triggers Envoy restart.
	if bootstrapChanged(desired.BootstrapFile, desiredBootstrapHash, meta) {
		input.BootstrapFile = desired.BootstrapFile
	}

	// Include ArchiveBytes if changed — no restart.
	if archiveChanged(desired.ArchiveBytes, desiredArchiveHash, meta) {
		input.ArchiveBytes = desired.ArchiveBytes
	}

	kmsChanged := kmsKeyChanged(resProxy.CustomerKmsKeyArn, sdkProxy.ProxyDetails.CustomerKmsKeyArn)

	if len(input.BootstrapFile) == 0 && len(input.ArchiveBytes) == 0 && !kmsChanged {
		r.logger.Info("proxy configuration unchanged, skipping", "arn", arn)
		return nil
	}

	// When the ARN changes, send both the bootstrap and the archive so the API
	// re-encrypts them under the new key.
	if kmsChanged {
		input.BootstrapFile = desired.BootstrapFile
		input.ArchiveBytes = desired.ArchiveBytes
	}

	// Set the desired ARN on every write — sent when set, omitted when nil.
	input.CustomerKmsKeyArn = resProxy.CustomerKmsKeyArn

	r.logger.Info("updating proxy configuration",
		"arn", arn,
		"bootstrapChanged", len(input.BootstrapFile) > 0,
		"archiveChanged", len(input.ArchiveBytes) > 0,
		"kmsChanged", kmsChanged,
	)
	r.logger.V(1).Info("proxy configuration update details",
		"arn", arn,
		"desiredBootstrapHash", desiredBootstrapHash,
		"desiredArchiveHash", desiredArchiveHash,
		"liveBootstrapHash", hashPtrValue(meta, func(m *elbproxyaws.ConfigurationMetadata) *string { return m.ConfigFileHash }),
		"liveArchiveHash", hashPtrValue(meta, func(m *elbproxyaws.ConfigurationMetadata) *string { return m.ConfigArchiveHash }),
		"desiredKmsKeyArn", strPtrValue(resProxy.CustomerKmsKeyArn),
		"liveKmsKeyArn", strPtrValue(sdkProxy.ProxyDetails.CustomerKmsKeyArn),
		"bootstrapBytes", len(input.BootstrapFile),
		"archiveBytes", len(input.ArchiveBytes),
	)
	input.ProxyIdentifier = arn
	if err := r.client.UpdateProxyConfiguration(ctx, input); err != nil {
		return fmt.Errorf("failed to update proxy configuration %s: %w", arn, err)
	}
	return nil
}

// kmsKeyChanged reports whether the desired CMK ARN differs from the live ARN.
// Both nil means the default service-owned key is in use on both sides.
func kmsKeyChanged(desired, live *string) bool {
	return strPtrValue(desired) != strPtrValue(live)
}

// strPtrValue returns the dereferenced string, or empty string when nil.
func strPtrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func hashBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func bootstrapChanged(desired []byte, desiredHash string, meta *elbproxyaws.ConfigurationMetadata) bool {
	if len(desired) == 0 {
		return false
	}
	if meta == nil || meta.ConfigFileHash == nil {
		return true
	}
	return desiredHash != *meta.ConfigFileHash
}

func archiveChanged(desired []byte, desiredHash string, meta *elbproxyaws.ConfigurationMetadata) bool {
	if len(desired) == 0 {
		return false
	}
	if meta == nil || meta.ConfigArchiveHash == nil {
		return true
	}
	return desiredHash != *meta.ConfigArchiveHash
}

// hashPtrValue safely extracts a hash string from ConfigurationMetadata.
// Returns empty string when metadata or the field is nil.
func hashPtrValue(meta *elbproxyaws.ConfigurationMetadata, field func(*elbproxyaws.ConfigurationMetadata) *string) string {
	if meta == nil {
		return ""
	}
	v := field(meta)
	if v == nil {
		return ""
	}
	return *v
}
