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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=internalpackagerevisions,singular=internalpackagerevision

// InternalPackageRevision
type InternalPackageRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InternalPackageRevisionSpec   `json:"spec,omitempty"`
	Status InternalPackageRevisionStatus `json:"status,omitempty"`
}

// InternalPackageRevisionSpec defines the desired state of InternalPackageRevision
type InternalPackageRevisionSpec struct {
}

// InternalPackageRevisionStatus defines the observed state of InternalPackageRevision
type InternalPackageRevisionStatus struct {
	// Conditions describes the reconciliation state of the object.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true

// InternalPackageRevisionList contains a list of InternalPackageRevision
type InternalPackageRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InternalPackageRevision `json:"items"`
}
