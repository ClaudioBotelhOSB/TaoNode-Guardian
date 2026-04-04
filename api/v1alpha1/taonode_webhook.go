// api/v1alpha1/taonode_webhook.go
/*
Copyright 2026 Claudio Botelho.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the TaoNode webhook with the controller-runtime manager.
func (r *TaoNode) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/validate-tao-guardian-io-v1alpha1-taonode,mutating=false,failurePolicy=Fail,sideEffects=None,groups=tao.guardian.io,resources=taonodes,verbs=create;update,versions=v1alpha1,name=vtaonode.kb.io,admissionReviewVersions=v1

// ValidateCreate implements webhook.Validator.
// Runs admission validation on TaoNode creation requests.
func (r *TaoNode) ValidateCreate() (admission.Warnings, error) {
	return r.validate(nil)
}

// ValidateUpdate implements webhook.Validator.
// Runs admission validation on TaoNode update requests,
// including immutability checks against the old object.
func (r *TaoNode) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	oldNode, ok := old.(*TaoNode)
	if !ok {
		return nil, fmt.Errorf("expected *TaoNode, got %T", old)
	}
	return r.validate(oldNode)
}

// ValidateDelete implements webhook.Validator.
// TaoNode deletion is always permitted at the webhook level;
// cleanup is handled by the controller's finalizer.
func (r *TaoNode) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

// validate runs all validation rules. oldNode is nil on creation.
func (r *TaoNode) validate(oldNode *TaoNode) (admission.Warnings, error) {
	var allErrs field.ErrorList

	// ── Key-management requirements ──────────────────────────────────────
	// Validator: hotkey is REQUIRED — cannot participate in consensus without one.
	// Miner:     hotkey is OPTIONAL — can run as unauthenticated full node;
	//            when a validator block IS present, validate its content.
	// Other:     spec.validator is FORBIDDEN — prevents accidental key exposure.
	switch r.Spec.Role {
	case RoleValidator:
		allErrs = append(allErrs, r.validateValidatorSpec(field.NewPath("spec"))...)
	case RoleMiner:
		if r.Spec.Validator != nil {
			allErrs = append(allErrs, r.validateValidatorSpec(field.NewPath("spec"))...)
		}
	default:
		if r.Spec.Validator != nil {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec", "validator"),
				"validator key material is only allowed for roles 'validator' or 'miner'",
			))
		}
	}

	// ── GPU type validation ───────────────────────────────────────────────
	if r.Spec.GPU != nil {
		allErrs = append(allErrs, r.validateGPUSpec(field.NewPath("spec", "gpu"))...)
	}

	// ── Minimum storage sizes by role ─────────────────────────────────────
	allErrs = append(allErrs, r.validateStorageSize(field.NewPath("spec", "chainStorage"))...)

	// ── Analytics spec consistency ────────────────────────────────────────
	if r.Spec.Analytics != nil && r.Spec.Analytics.Enabled {
		allErrs = append(allErrs, r.validateAnalyticsSpec(field.NewPath("spec", "analytics"))...)
	}

	// ── Immutability checks (only on update) ─────────────────────────────
	if oldNode != nil {
		allErrs = append(allErrs, r.validateImmutableFields(oldNode, field.NewPath("spec"))...)
	}

	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "tao.guardian.io", Kind: "TaoNode"},
			r.Name,
			allErrs,
		)
	}
	return nil, nil
}

// validateValidatorSpec checks validator-specific rules.
func (r *TaoNode) validateValidatorSpec(fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if r.Spec.Validator == nil {
		errs = append(errs, field.Required(
			fldPath.Child("validator"),
			"validator spec is required when role is 'validator'",
		))
		return errs
	}

	v := r.Spec.Validator

	if v.HotKeySecret == "" {
		errs = append(errs, field.Required(
			fldPath.Child("validator", "hotKeySecret"),
			"hotKeySecret must be specified for validator nodes",
		))
	}

	// On mainnet: slashing protection must be enabled.
	if r.Spec.Network == "mainnet" && !v.SlashingProtection {
		errs = append(errs, field.Invalid(
			fldPath.Child("validator", "slashingProtection"),
			false,
			"slashing protection cannot be disabled on mainnet (risk of double-signing penalties)",
		))
	}

	// On mainnet: exactly 1 concurrent validator allowed.
	if r.Spec.Network == "mainnet" && v.MaxConcurrentValidators != 1 {
		errs = append(errs, field.Invalid(
			fldPath.Child("validator", "maxConcurrentValidators"),
			v.MaxConcurrentValidators,
			"must be exactly 1 on mainnet to prevent slashing from duplicate validators",
		))
	}

	return errs
}

// validateGPUSpec checks that the GPU type is a recognized value.
func (r *TaoNode) validateGPUSpec(fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	validGPUTypes := sets.New(
		"nvidia-a100",
		"nvidia-h100",
		"nvidia-t4",
		"nvidia-l4",
		"nvidia-a10g",
		"nvidia-v100",
		"amd-gpu",
	)

	if !validGPUTypes.Has(r.Spec.GPU.Type) {
		errs = append(errs, field.NotSupported(
			fldPath.Child("type"),
			r.Spec.GPU.Type,
			sets.List(validGPUTypes),
		))
	}

	if r.Spec.GPU.Count < 1 {
		errs = append(errs, field.Invalid(
			fldPath.Child("count"),
			r.Spec.GPU.Count,
			"GPU count must be at least 1",
		))
	}

	return errs
}

// validateStorageSize enforces minimum PVC sizes per role.
// Chain data sizes are non-trivial: subtensor requires at least 100Gi.
func (r *TaoNode) validateStorageSize(fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	minSizes := map[TaoNodeRole]int64{
		RoleSubtensor: 100 * 1024 * 1024 * 1024, // 100Gi
		RoleMiner:     50 * 1024 * 1024 * 1024,  // 50Gi
		RoleValidator: 100 * 1024 * 1024 * 1024, // 100Gi
	}

	if min, ok := minSizes[r.Spec.Role]; ok {
		if r.Spec.ChainStorage.Size.Value() < min {
			minGiB := min / (1024 * 1024 * 1024)
			errs = append(errs, field.Invalid(
				fldPath.Child("size"),
				r.Spec.ChainStorage.Size.String(),
				fmt.Sprintf("minimum storage for %s role is %dGi", r.Spec.Role, minGiB),
			))
		}
	}

	// Validate autoExpand thresholds if configured.
	if ae := r.Spec.ChainStorage.AutoExpand; ae != nil {
		if ae.MaxSize.Cmp(r.Spec.ChainStorage.Size) < 0 {
			errs = append(errs, field.Invalid(
				fldPath.Child("autoExpand", "maxSize"),
				ae.MaxSize.String(),
				"autoExpand.maxSize must be greater than or equal to the initial size",
			))
		}
	}

	return errs
}

// validateAnalyticsSpec checks that required analytics fields are present
// when analytics is enabled.
func (r *TaoNode) validateAnalyticsSpec(fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	a := r.Spec.Analytics

	if a.ClickHouseRef.Endpoint == "" {
		errs = append(errs, field.Required(
			fldPath.Child("clickhouseRef", "endpoint"),
			"clickhouseRef.endpoint is required when analytics is enabled",
		))
	}
	if a.ClickHouseRef.CredentialsSecret == "" {
		errs = append(errs, field.Required(
			fldPath.Child("clickhouseRef", "credentialsSecret"),
			"clickhouseRef.credentialsSecret is required when analytics is enabled",
		))
	}

	return errs
}

// validateImmutableFields enforces that network and subnetID cannot be changed
// after creation, and that storage size can only grow (never shrink).
func (r *TaoNode) validateImmutableFields(old *TaoNode, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if r.Spec.Network != old.Spec.Network {
		errs = append(errs, field.Forbidden(
			fldPath.Child("network"),
			"network is immutable after creation — create a new TaoNode to change networks",
		))
	}

	if r.Spec.SubnetID != old.Spec.SubnetID {
		errs = append(errs, field.Forbidden(
			fldPath.Child("subnetID"),
			"subnetID is immutable after creation",
		))
	}

	if r.Spec.ChainStorage.Size.Cmp(old.Spec.ChainStorage.Size) < 0 {
		errs = append(errs, field.Forbidden(
			fldPath.Child("chainStorage", "size"),
			fmt.Sprintf("storage size can only be increased (current: %s)",
				old.Spec.ChainStorage.Size.String()),
		))
	}

	return errs
}
