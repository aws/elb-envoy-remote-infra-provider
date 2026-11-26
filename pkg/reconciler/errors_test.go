// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsRequeueAfterReason(t *testing.T) {
	deleting := NewRequeueNeededAfter(RequeueReasonPreviousProxyDeleting, 30*time.Second)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "matching reason", err: deleting, want: true},
		{name: "matching reason wrapped", err: fmt.Errorf("deploy stack: %w", deleting), want: true},
		{name: "other requeue reason", err: NewRequeueNeededAfter("proxy provisioning is still in progress", time.Minute), want: false},
		{name: "plain error", err: errors.New(RequeueReasonPreviousProxyDeleting), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsRequeueAfterReason(tt.err, RequeueReasonPreviousProxyDeleting))
		})
	}
}
