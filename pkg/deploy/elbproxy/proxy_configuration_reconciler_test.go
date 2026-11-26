// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
)

func strPtr(s string) *string { return &s }

func TestHashBytes(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"nil input returns empty", nil, ""},
		{"empty slice returns empty", []byte{}, ""},
		{"non-empty returns hex sha256", []byte("hello"), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"same input same hash", []byte("hello"), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hashBytes(tt.input))
		})
	}
}

func TestBootstrapChanged(t *testing.T) {
	content := []byte("bootstrap-config")
	hash := hashBytes(content)

	tests := []struct {
		name    string
		desired []byte
		meta    *elbproxyaws.ConfigurationMetadata
		want    bool
	}{
		{"empty desired — no change", nil, nil, false},
		{"nil meta — changed", content, nil, true},
		{"nil hash in meta — changed", content, &elbproxyaws.ConfigurationMetadata{}, true},
		{"hash matches — no change", content, &elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(hash)}, false},
		{"hash differs — changed", content, &elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr("old-hash")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bootstrapChanged(tt.desired, hashBytes(tt.desired), tt.meta))
		})
	}
}

func TestArchiveChanged(t *testing.T) {
	content := []byte("archive-data")
	hash := hashBytes(content)

	tests := []struct {
		name    string
		desired []byte
		meta    *elbproxyaws.ConfigurationMetadata
		want    bool
	}{
		{"empty desired — no change", nil, nil, false},
		{"nil meta — changed", content, nil, true},
		{"nil hash in meta — changed", content, &elbproxyaws.ConfigurationMetadata{}, true},
		{"hash matches — no change", content, &elbproxyaws.ConfigurationMetadata{ConfigArchiveHash: strPtr(hash)}, false},
		{"hash differs — changed", content, &elbproxyaws.ConfigurationMetadata{ConfigArchiveHash: strPtr("old-hash")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, archiveChanged(tt.desired, hashBytes(tt.desired), tt.meta))
		})
	}
}

func TestKmsKeyChanged(t *testing.T) {
	tests := []struct {
		name    string
		desired *string
		live    *string
		want    bool
	}{
		{"both nil — no change", nil, nil, false},
		{"both same ARN — no change", strPtr("arn:kms:a"), strPtr("arn:kms:a"), false},
		{"desired set, live nil — changed", strPtr("arn:kms:a"), nil, true},
		{"desired nil, live set — changed", nil, strPtr("arn:kms:a"), true},
		{"different ARNs — changed", strPtr("arn:kms:a"), strPtr("arn:kms:b"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, kmsKeyChanged(tt.desired, tt.live))
		})
	}
}

func TestProxyConfigurationReconciler_Reconcile(t *testing.T) {
	bootstrap := []byte("bootstrap")
	archive := []byte("archive")
	bootstrapHash := hashBytes(bootstrap)
	archiveHash := hashBytes(archive)

	sdkProxy := func(meta *elbproxyaws.ConfigurationMetadata) ProxyWithTags {
		return ProxyWithTags{
			ProxyDetails: elbproxyaws.ProxyDetails{
				Arn:                   "arn:proxy-1",
				ConfigurationMetadata: meta,
			},
		}
	}
	// sdkProxyKms builds an SDK proxy with a live CMK ARN alongside the config metadata.
	sdkProxyKms := func(meta *elbproxyaws.ConfigurationMetadata, liveKms *string) ProxyWithTags {
		p := sdkProxy(meta)
		p.ProxyDetails.CustomerKmsKeyArn = liveKms
		return p
	}
	resProxy := func(b, a []byte) *elbproxymodel.ProxyResource {
		return &elbproxymodel.ProxyResource{
			Configuration: elbproxyaws.ProxyConfiguration{
				BootstrapFile: b,
				ArchiveBytes:  a,
			},
		}
	}
	// resProxyKms builds a resource proxy with a desired CMK ARN.
	resProxyKms := func(b, a []byte, desiredKms *string) *elbproxymodel.ProxyResource {
		r := resProxy(b, a)
		r.CustomerKmsKeyArn = desiredKms
		return r
	}

	tests := []struct {
		name          string
		resProxy      *elbproxymodel.ProxyResource
		sdkProxy      ProxyWithTags
		expectCall    bool
		expectErr     bool
		clientErr     error
		wantBootstrap []byte
		wantArchive   []byte
		wantKms       *string
	}{
		{
			name:       "nothing changed — no API call",
			resProxy:   resProxy(bootstrap, archive),
			sdkProxy:   sdkProxy(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(bootstrapHash), ConfigArchiveHash: strPtr(archiveHash)}),
			expectCall: false,
		},
		{
			name:          "bootstrap changed — API call with bootstrap only",
			resProxy:      resProxy(bootstrap, archive),
			sdkProxy:      sdkProxy(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr("old"), ConfigArchiveHash: strPtr(archiveHash)}),
			expectCall:    true,
			wantBootstrap: bootstrap,
			wantArchive:   nil,
		},
		{
			name:          "archive changed — API call with archive only",
			resProxy:      resProxy(bootstrap, archive),
			sdkProxy:      sdkProxy(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(bootstrapHash), ConfigArchiveHash: strPtr("old")}),
			expectCall:    true,
			wantBootstrap: nil,
			wantArchive:   archive,
		},
		{
			// IR-absent shape: the builder produced an archive-only configuration
			// (empty BootstrapFile). Even though the live bootstrap hash differs,
			// an empty desired bootstrap must NEVER be pushed — the deployed
			// bootstrap is preserved. Only the changed archive is sent.
			name:          "IR absent (empty bootstrap) — archive only, bootstrap preserved",
			resProxy:      resProxy(nil, archive),
			sdkProxy:      sdkProxy(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr("live-bootstrap-hash"), ConfigArchiveHash: strPtr("old")}),
			expectCall:    true,
			wantBootstrap: nil,
			wantArchive:   archive,
		},
		{
			// IR-absent shape with nothing else changed: empty bootstrap + matching
			// archive hash means no API call at all.
			name:       "IR absent (empty bootstrap) — archive unchanged — no API call",
			resProxy:   resProxy(nil, archive),
			sdkProxy:   sdkProxy(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr("live-bootstrap-hash"), ConfigArchiveHash: strPtr(archiveHash)}),
			expectCall: false,
		},
		{
			name:          "both changed — API call with both",
			resProxy:      resProxy(bootstrap, archive),
			sdkProxy:      sdkProxy(nil),
			expectCall:    true,
			wantBootstrap: bootstrap,
			wantArchive:   archive,
		},
		{
			name:          "API error — error returned",
			resProxy:      resProxy(bootstrap, nil),
			sdkProxy:      sdkProxy(nil),
			expectCall:    true,
			clientErr:     fmt.Errorf("API error"),
			expectErr:     true,
			wantBootstrap: bootstrap,
			wantArchive:   nil,
		},
		{
			name:          "KMS set, config unchanged — full config re-sent (Default->CMK)",
			resProxy:      resProxyKms(bootstrap, archive, strPtr("arn:kms:new")),
			sdkProxy:      sdkProxyKms(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(bootstrapHash), ConfigArchiveHash: strPtr(archiveHash)}, nil),
			expectCall:    true,
			wantBootstrap: bootstrap,
			wantArchive:   archive,
			wantKms:       strPtr("arn:kms:new"),
		},
		{
			name:          "KMS rotated, config unchanged — full config re-sent (CMK->CMK)",
			resProxy:      resProxyKms(bootstrap, archive, strPtr("arn:kms:new")),
			sdkProxy:      sdkProxyKms(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(bootstrapHash), ConfigArchiveHash: strPtr(archiveHash)}, strPtr("arn:kms:old")),
			expectCall:    true,
			wantBootstrap: bootstrap,
			wantArchive:   archive,
			wantKms:       strPtr("arn:kms:new"),
		},
		{
			name:          "KMS cleared, config unchanged — full config re-sent (CMK->Default)",
			resProxy:      resProxyKms(bootstrap, archive, nil),
			sdkProxy:      sdkProxyKms(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(bootstrapHash), ConfigArchiveHash: strPtr(archiveHash)}, strPtr("arn:kms:old")),
			expectCall:    true,
			wantBootstrap: bootstrap,
			wantArchive:   archive,
			wantKms:       nil,
		},
		{
			// Key matches live and config matches — no drift at all, no API call.
			name:       "KMS unchanged, config unchanged — no API call",
			resProxy:   resProxyKms(bootstrap, archive, strPtr("arn:kms:same")),
			sdkProxy:   sdkProxyKms(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr(bootstrapHash), ConfigArchiveHash: strPtr(archiveHash)}, strPtr("arn:kms:same")),
			expectCall: false,
		},
		{
			// Bootstrap changed AND desired key set: the normal change path sends the
			// changed bootstrap, and the desired key is attached to that same write.
			name:          "bootstrap changed with KMS set — key attached to write",
			resProxy:      resProxyKms(bootstrap, archive, strPtr("arn:kms:new")),
			sdkProxy:      sdkProxyKms(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr("old"), ConfigArchiveHash: strPtr(archiveHash)}, strPtr("arn:kms:new")),
			expectCall:    true,
			wantBootstrap: bootstrap,
			wantArchive:   nil,
			wantKms:       strPtr("arn:kms:new"),
		},
		{
			// Key change on an archive-only (empty bootstrap) proxy: the key-change
			// re-send must still not push an empty bootstrap. Only the archive is
			// sent, with the new key attached.
			name:          "KMS set, archive-only proxy — empty bootstrap not pushed",
			resProxy:      resProxyKms(nil, archive, strPtr("arn:kms:new")),
			sdkProxy:      sdkProxyKms(&elbproxyaws.ConfigurationMetadata{ConfigFileHash: strPtr("live-bootstrap-hash"), ConfigArchiveHash: strPtr(archiveHash)}, nil),
			expectCall:    true,
			wantBootstrap: nil,
			wantArchive:   archive,
			wantKms:       strPtr("arn:kms:new"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
			if tt.expectCall {
				mockClient.EXPECT().
					UpdateProxyConfiguration(gomock.Any(), gomock.AssignableToTypeOf(elbproxyaws.UpdateProxyConfigurationInput{})).
					DoAndReturn(func(_ context.Context, input elbproxyaws.UpdateProxyConfigurationInput) error {
						assert.Equal(t, "arn:proxy-1", input.ProxyIdentifier)
						assert.Equal(t, tt.wantBootstrap, input.BootstrapFile)
						assert.Equal(t, tt.wantArchive, input.ArchiveBytes)
						assert.Equal(t, tt.wantKms, input.CustomerKmsKeyArn)
						return tt.clientErr
					})
			}

			rec := NewDefaultProxyConfigurationReconciler(mockClient, testLogger())
			err := rec.Reconcile(context.Background(), tt.resProxy, tt.sdkProxy)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
