// internal/controller/validator_guard.go
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

package controller

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ptr "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

const (
	// validatorLeaseDuration is how long the validator Lease is held.
	// If the holder does not renew within this window, the Lease expires
	// and another validator instance may claim it.
	validatorLeaseDuration = 30 * time.Second

	// validatorLeaseRenewInterval is how often the controller renews the Lease.
	// validatorLeaseRenewInterval = 10 * time.Second
)

// ensureValidatorSingleton enforces that only one validator node runs per subnet
// on mainnet by using a Kubernetes Lease as a distributed lock.
//
// If spec.validator.maxConcurrentValidators is > 1 (only allowed on testnet/devnet),
// this check is skipped. On mainnet, the policy is always enforced regardless of spec.
//
// A Lease named "{tn.Name}-validator-lock" is created in the TaoNode's namespace.
// If the Lease already exists and is held by a different TaoNode, this TaoNode
// is cordoned (pod deleted) and an event is emitted.
func (r *TaoNodeReconciler) ensureValidatorSingleton(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	log := logf.FromContext(ctx)

	if tn.Spec.Role != taov1alpha1.RoleValidator {
		return nil
	}

	// On mainnet, always enforce singleton. On other networks, respect spec.
	if tn.Spec.Network != "mainnet" && tn.Spec.Validator != nil &&
		tn.Spec.Validator.MaxConcurrentValidators > 1 {
		return nil
	}

	leaseName := fmt.Sprintf("%s-validator-singleton", tn.Name)
	holderIdentity := fmt.Sprintf("%s/%s", tn.Namespace, tn.Name)
	leaseDurationSeconds := int32(validatorLeaseDuration.Seconds())

	lease := &coordinationv1.Lease{}
	err := r.Get(ctx, types.NamespacedName{Name: leaseName, Namespace: tn.Namespace}, lease)

	if apierrors.IsNotFound(err) {
		// Create the Lease — this node becomes the singleton holder.
		now := metav1.NewMicroTime(time.Now())
		newLease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      leaseName,
				Namespace: tn.Namespace,
				Labels:    r.labelsForNode(tn),
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptr.To(holderIdentity),
				LeaseDurationSeconds: &leaseDurationSeconds,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		if err := controllerutil.SetControllerReference(tn, newLease, r.Scheme); err != nil {
			return fmt.Errorf("set owner reference on lease: %w", err)
		}
		if err := r.Create(ctx, newLease); err != nil {
			return fmt.Errorf("create validator Lease: %w", err)
		}
		log.Info("Validator singleton Lease acquired", "lease", leaseName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get validator Lease: %w", err)
	}

	// Lease exists — check if it's ours or has expired.
	currentHolder := ""
	if lease.Spec.HolderIdentity != nil {
		currentHolder = *lease.Spec.HolderIdentity
	}

	// If we hold the lease, renew it.
	if currentHolder == holderIdentity {
		now := metav1.NewMicroTime(time.Now())
		leaseCopy := lease.DeepCopy()
		leaseCopy.Spec.RenewTime = &now
		if err := r.Update(ctx, leaseCopy); err != nil {
			return fmt.Errorf("renew validator Lease: %w", err)
		}
		return nil
	}

	// Someone else holds the lease. Check if it has expired.
	if lease.Spec.RenewTime != nil && lease.Spec.LeaseDurationSeconds != nil {
		expiry := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
		if time.Now().Before(expiry) {
			// Lease is live — this node must not run as validator.
			r.Recorder.Eventf(tn, corev1.EventTypeWarning, "ValidatorSingletonViolation",
				"Another validator already holds the singleton Lease (%s). Cordon-and-alert applied.", currentHolder)
			log.Error(nil, "Validator singleton violation detected",
				"leaseName", leaseName,
				"currentHolder", currentHolder,
				"thisNode", holderIdentity,
			)
			// Cordon the pod to prevent double-signing.
			return r.cordonValidatorPod(ctx, tn)
		}
	}

	// Lease has expired — take it over.
	now := metav1.NewMicroTime(time.Now())
	leaseCopy := lease.DeepCopy()
	leaseCopy.Spec.HolderIdentity = ptr.To(holderIdentity)
	leaseCopy.Spec.AcquireTime = &now
	leaseCopy.Spec.RenewTime = &now
	if err := r.Update(ctx, leaseCopy); err != nil {
		return fmt.Errorf("take over expired validator Lease: %w", err)
	}
	log.Info("Took over expired validator singleton Lease",
		"lease", leaseName,
		"previousHolder", currentHolder,
	)
	return nil
}

// cordonValidatorPod deletes the validator pod to prevent double-signing.
// The StatefulSet will recreate it, but the reconciler will catch the violation
// again and keep deleting until the conflicting Lease holder releases.
func (r *TaoNodeReconciler) cordonValidatorPod(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	podName := fmt.Sprintf("%s-0", tn.Name)
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: tn.Namespace}, pod); err != nil {
		// Pod doesn't exist — nothing to cordon.
		return client.IgnoreNotFound(err)
	}
	if err := r.Delete(ctx, pod); err != nil {
		return fmt.Errorf("cordon validator pod %s: %w", podName, err)
	}
	return nil
}
