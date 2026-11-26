// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Mutator defines the interface for a mutating admission webhook.
type Mutator interface {
	// Prototype returns a prototype Object for this admission request.
	Prototype(req admission.Request) (runtime.Object, error)

	// MutateCreate handles Object creation and returns the mutated object (and error if any).
	MutateCreate(ctx context.Context, obj runtime.Object) (runtime.Object, error)
	// MutateUpdate handles Object update and returns the mutated object (and error if any).
	MutateUpdate(ctx context.Context, obj runtime.Object, oldObj runtime.Object) (runtime.Object, error)
}

// MutatingWebhookForMutator wraps a Mutator into a controller-runtime admission.Webhook.
func MutatingWebhookForMutator(mutator Mutator, scheme *runtime.Scheme) *admission.Webhook {
	return &admission.Webhook{
		Handler: &mutatingHandler{
			mutator: mutator,
			decoder: admission.NewDecoder(scheme),
		},
	}
}
