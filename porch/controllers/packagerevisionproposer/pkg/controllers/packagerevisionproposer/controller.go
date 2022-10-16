// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packagerevisionproposer

import (
	"context"

	"github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	EnableAnnotation = "config.porch.kpt.dev/auto-lifecycle-mgmt"
)

// PackageRevisionProposerReconciler reconciles PackageRevision objects
type PackageRevisionProposerReconciler struct {
	client.Client
}

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.8.0 rbac:roleName=porch-controllers-packagerevisionproposer webhook paths="." output:rbac:artifacts:config=../../../config/rbac

//+kubebuilder:rbac:groups=porch.kpt.dev,resources=packagerevisions,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main kubernetes reconciliation loop.
func (r *PackageRevisionProposerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var subject v1alpha1.PackageRevision
	if err := r.Get(ctx, req.NamespacedName, &subject); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if subject.Spec.Lifecycle != v1alpha1.PackageRevisionLifecycleDraft {
		return ctrl.Result{}, nil
	}

	// TODO: Enable this when we have support for annotations.
	// if val, found := subject.Annotations[EnableAnnotation]; !found || val != "true" {
	// 	klog.Infof("PackageRevision %s is not enabled", req.NamespacedName)
	// 	return ctrl.Result{}, nil
	// }

	readinessGates := subject.Spec.ReadinessGates
	conditions := subject.Status.Conditions

	// TODO: This is a temporary workaround until we have support for annotations.
	enabled := false
	for _, rg := range readinessGates {
		if rg.ConditionType == "auto-lifecycle-mgmt" {
			enabled = true
		}
	}
	if !enabled {
		klog.Infof("PackageRevision %v is not enabled", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	klog.Infof("PackageRevision %v is enabled. Checking if gates are satisfied", req.NamespacedName)
	for _, rg := range readinessGates {
		conditionType := rg.ConditionType
		if conditionType == "auto-lifecycle-mgmt" {
			continue
		}
		satisfied := false
		for _, c := range conditions {
			if c.Type == conditionType && c.Status == v1alpha1.ConditionTrue {
				satisfied = true
			}
		}
		if !satisfied {
			klog.Infof("PackageRevision %v does not have readinessGate %v enabled", req.NamespacedName, conditionType)
			return ctrl.Result{}, nil
		}
	}

	klog.Infof("Updating packagerevision %v", req.NamespacedName)
	subject.Spec.Lifecycle = v1alpha1.PackageRevisionLifecycleProposed
	if err := r.Update(ctx, &subject); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageRevisionProposerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := v1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	r.Client = mgr.GetClient()

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PackageRevision{}).
		Complete(r); err != nil {
		return err
	}

	return nil
}
