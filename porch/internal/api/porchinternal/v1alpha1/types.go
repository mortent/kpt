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
	"github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=packagerevs,singular=packagerev

// PackageRev
type PackageRev struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageRevSpec   `json:"spec,omitempty"`
	Status PackageRevStatus `json:"status,omitempty"`
}

// PackageRevSpec defines the desired state of PackageRev
type PackageRevSpec struct {
	// PackageName identifies the package in the repository.
	PackageName string `json:"packageName,omitempty"`

	// RepositoryName is the name of the Repository object containing this package.
	RepositoryName string `json:"repository,omitempty"`

	// WorkspaceName is a short, unique description of the changes contained in this package revision.
	WorkspaceName v1alpha1.WorkspaceName `json:"workspaceName,omitempty"`

	// Revision identifies the version of the package.
	Revision string `json:"revision,omitempty"`

	// Parent references a package that provides resources to us
	Parent *v1alpha1.ParentReference `json:"parent,omitempty"`

	Lifecycle v1alpha1.PackageRevisionLifecycle `json:"lifecycle,omitempty"`

	Tasks []v1alpha1.Task `json:"tasks,omitempty"`

	ReadinessGates []v1alpha1.ReadinessGate `json:"readinessGates,omitempty"`
}

// PackageRevStatus defines the observed state of PackageRev
type PackageRevStatus struct {
}

//+kubebuilder:object:root=true

// PackageRevList contains a list of PackageRev
type PackageRevList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PackageRev `json:"items"`
}
