/*
 * SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package webhook

import (
	"context"
	"strings"

	"github.com/ai-dynamo/dynamo/deploy/operator/internal/consts"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var webhookCommonLog = logf.Log.WithName("webhook-common")

// ExcludedNamespacesChecker reports whether a namespace has an active
// namespaced-operator reconciliation claim.
type ExcludedNamespacesChecker interface {
	Contains(namespace string) bool
}

var webhookExcludedNamespaces ExcludedNamespacesChecker

// SetExcludedNamespaces sets the namespace claims consulted by validating webhooks.
// It must be called before webhook handlers are registered.
func SetExcludedNamespaces(checker ExcludedNamespacesChecker) {
	webhookExcludedNamespaces = checker
}

// GetExcludedNamespaces returns the namespace claims consulted by validating webhooks.
func GetExcludedNamespaces() ExcludedNamespacesChecker {
	return webhookExcludedNamespaces
}

// LeaseAwareValidator lets the cluster-wide validator stand down when an active
// namespace claim transfers validation to a development/test namespaced operator.
type LeaseAwareValidator struct {
	validator          admission.CustomValidator
	excludedNamespaces ExcludedNamespacesChecker
}

// NewLeaseAwareValidator wraps validator with namespace-claim handling. A nil
// checker returns the validator unchanged, as used by namespaced validation.
func NewLeaseAwareValidator(
	validator admission.CustomValidator,
	excludedNamespaces ExcludedNamespacesChecker,
) admission.CustomValidator {
	if excludedNamespaces == nil {
		return validator
	}

	return &LeaseAwareValidator{
		validator:          validator,
		excludedNamespaces: excludedNamespaces,
	}
}

// ValidateCreate implements admission.CustomValidator.
func (v *LeaseAwareValidator) ValidateCreate(
	ctx context.Context,
	obj runtime.Object,
) (admission.Warnings, error) {
	if v.shouldSkipValidation(obj) {
		return nil, nil
	}

	return v.validator.ValidateCreate(ctx, obj)
}

// ValidateUpdate implements admission.CustomValidator.
func (v *LeaseAwareValidator) ValidateUpdate(
	ctx context.Context,
	oldObj runtime.Object,
	newObj runtime.Object,
) (admission.Warnings, error) {
	if v.shouldSkipValidation(newObj) {
		return nil, nil
	}

	return v.validator.ValidateUpdate(ctx, oldObj, newObj)
}

// ValidateDelete implements admission.CustomValidator.
func (v *LeaseAwareValidator) ValidateDelete(
	ctx context.Context,
	obj runtime.Object,
) (admission.Warnings, error) {
	if v.shouldSkipValidation(obj) {
		return nil, nil
	}

	return v.validator.ValidateDelete(ctx, obj)
}

func (v *LeaseAwareValidator) shouldSkipValidation(obj runtime.Object) bool {
	clientObj, ok := obj.(client.Object)
	if !ok {
		return false
	}

	namespace := clientObj.GetNamespace()
	if !v.excludedNamespaces.Contains(namespace) {
		return false
	}

	webhookCommonLog.Info("skipping cluster-wide validation; namespace has an active namespaced-operator claim",
		"name", clientObj.GetName(),
		"namespace", namespace,
		"kind", obj.GetObjectKind().GroupVersionKind().Kind)
	return true
}

// CanModifyDGDReplicas checks if the request comes from a service account authorized
// to modify DGD replicas when scaling adapter is enabled.
//
// operatorPrincipal is the full Kubernetes username
// (system:serviceaccount:<namespace>:<name>) of the operator's own service account,
// auto-detected at startup via the Kubernetes Downward API. It may be empty if
// the Downward API env vars were not set.
//
// Authorization is checked in two ways:
//  1. Exact match against operatorPrincipal.
//  2. Name-only match for the planner SA, which the operator creates in every DGD
//     namespace with a well-known constant name. Because the namespace is only known
//     at runtime, it cannot be enumerated statically.
func CanModifyDGDReplicas(operatorPrincipal string, userInfo authenticationv1.UserInfo) bool {
	username := userInfo.Username

	if !strings.HasPrefix(username, "system:serviceaccount:") {
		return false
	}

	if operatorPrincipal != "" && username == operatorPrincipal {
		webhookCommonLog.V(1).Info("allowing DGD replicas modification",
			"username", username,
			"matchType", "operatorPrincipal")
		return true
	}

	parts := strings.Split(username, ":")
	if len(parts) == 4 && parts[3] == consts.PlannerServiceAccountName {
		webhookCommonLog.V(1).Info("allowing DGD replicas modification",
			"username", username,
			"matchType", "plannerSA")
		return true
	}

	return false
}
