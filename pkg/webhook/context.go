// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type contextKey string

const (
	contextKeyAdmissionRequest contextKey = "admissionRequest"
)

// ContextGetAdmissionRequest returns the admission.Request stored on ctx, or nil.
func ContextGetAdmissionRequest(ctx context.Context) *admission.Request {
	if v := ctx.Value(contextKeyAdmissionRequest); v != nil {
		return v.(*admission.Request)
	}
	return nil
}

// ContextWithAdmissionRequest returns a copy of ctx carrying the admission.Request.
func ContextWithAdmissionRequest(ctx context.Context, req admission.Request) context.Context {
	return context.WithValue(ctx, contextKeyAdmissionRequest, &req)
}
