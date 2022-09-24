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

package meta

import (
	"context"

	configapi "github.com/GoogleContainerTools/kpt/porch/api/porchconfig/v1alpha1"
	internalapi "github.com/GoogleContainerTools/kpt/porch/internal/api/porchinternal/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MetadataStore interface {
	Get(ctx context.Context, name, namespace string) (*internalapi.InternalPackageRevision, error)
	Create(ctx context.Context, name, namespace string, labels, annos map[string]string, repo *configapi.Repository) (*internalapi.InternalPackageRevision, error)
	Update(ctx context.Context, name, namespace string, labels, annos map[string]string) (*internalapi.InternalPackageRevision, error)
	Delete(ctx context.Context, name, namespace string) (*internalapi.InternalPackageRevision, error)
}

var _ MetadataStore = &crdMetadataStore{}

func NewCrdMetadataStore(coreClient client.Client) *crdMetadataStore {
	return &crdMetadataStore{
		coreClient: coreClient,
	}
}

type crdMetadataStore struct {
	coreClient client.Client
}

func (c *crdMetadataStore) Get(ctx context.Context, name, namespace string) (*internalapi.InternalPackageRevision, error) {
	var internalPkgRev internalapi.InternalPackageRevision
	err := c.coreClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &internalPkgRev)
	if err != nil {
		return nil, err
	}
	return &internalPkgRev, nil
}

func (c *crdMetadataStore) Create(ctx context.Context, name, namespace string, labels, annos map[string]string, repo *configapi.Repository) (*internalapi.InternalPackageRevision, error) {
	internalPkgRev := internalapi.InternalPackageRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annos,
			// We probably should make these owner refs point to the PackageRevision CRs instead.
			// But we need to make sure that deletion of these are correctly picked up by the
			// GC.
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configapi.RepositoryGVK.GroupVersion().String(),
					Kind:       configapi.RepositoryGVK.Kind,
					Name:       repo.Name,
					UID:        repo.UID,
				},
			},
		},
	}
	if err := c.coreClient.Create(ctx, &internalPkgRev); err != nil {
		return nil, err
	}
	return &internalPkgRev, nil
}

func (c *crdMetadataStore) Update(ctx context.Context, name, namespace string, labels, annos map[string]string) (*internalapi.InternalPackageRevision, error) {
	internalPkgRev, err := c.Get(ctx, name, namespace)
	if err != nil {
		return nil, err
	}
	internalPkgRev.Labels = labels
	internalPkgRev.Annotations = annos
	err = c.coreClient.Update(ctx, internalPkgRev)
	return internalPkgRev, err
}

func (c *crdMetadataStore) Delete(ctx context.Context, name, namespace string) (*internalapi.InternalPackageRevision, error) {
	var internalPkgRev internalapi.InternalPackageRevision
	err := c.coreClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &internalPkgRev)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	if err := c.coreClient.Delete(ctx, &internalPkgRev); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &internalPkgRev, nil
}

// func (r *packageCommon) toApiPackageRevision(repoPackageRevision repository.PackageRevision, internalPkgRev *internalapi.InternalPackageRevision) *api.PackageRevision {
// 	apiPkgRev := repoPackageRevision.GetPackageRevision()
// 	r.amendApiPkgRevWithMetadata(apiPkgRev, internalPkgRev)
// 	return apiPkgRev
// }

// func (r *packageCommon) amendApiPkgRevWithMetadata(apiPkgRev *api.PackageRevision, internalPkgRev *internalapi.InternalPackageRevision) {
// 	apiPkgRev.Labels = internalPkgRev.Labels
// 	apiPkgRev.Annotations = internalPkgRev.Annotations
// }

// func (r *packageCommon) getApiPkgRev(ctx context.Context, repoPkgRev repository.PackageRevision, ns string) (*api.PackageRevision, error) {
// 	internalPkgRev, err := r.getInternalPkgRev(ctx, repoPkgRev.KubeObjectName(), ns)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return r.toApiPackageRevision(repoPkgRev, internalPkgRev), nil
// }

// func (r *packageCommon) getInternalPkgRev(ctx context.Context, name, ns string) (*internalapi.InternalPackageRevision, error) {
// 	var prs internalapi.InternalPackageRevision
// 	err := r.coreClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &prs)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &prs, nil
// }

// func (r *packageCommon) createInternalPkgRev(ctx context.Context, createdApiPkgRev, newApiPkgRev *api.PackageRevision) (*internalapi.InternalPackageRevision, error) {
// 	internalPkgRev := internalapi.InternalPackageRevision{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:        createdApiPkgRev.Name,
// 			Namespace:   createdApiPkgRev.Namespace,
// 			Labels:      newApiPkgRev.Labels,
// 			Annotations: newApiPkgRev.Annotations,
// 		},
// 	}
// 	if err := r.coreClient.Create(ctx, &internalPkgRev); err != nil {
// 		return nil, err
// 	}
// 	return &internalPkgRev, nil
// }

// func (r *packageCommon) updateInternalPkgRevFromPkgRev(ctx context.Context, internalPkgRev *internalapi.InternalPackageRevision, updatedApiPkgRev *api.PackageRevision) (*internalapi.InternalPackageRevision, error) {
// 	newInternalPkgRev := internalPkgRev.DeepCopy()
// 	newInternalPkgRev.Labels = updatedApiPkgRev.Labels
// 	newInternalPkgRev.Annotations = updatedApiPkgRev.Annotations
// 	err := r.coreClient.Update(ctx, newInternalPkgRev)
// 	return newInternalPkgRev, err
// }

// func (r *packageCommon) updateInternalPkgRevFromPkgResources(ctx context.Context, internalPkgRev *internalapi.InternalPackageRevision, updatedApiPkgResources *api.PackageRevisionResources) (*internalapi.InternalPackageRevision, error) {
// 	newInternalPkgRev := internalPkgRev.DeepCopy()
// 	newInternalPkgRev.Labels = updatedApiPkgResources.Labels
// 	newInternalPkgRev.Annotations = updatedApiPkgResources.Annotations
// 	err := r.coreClient.Update(ctx, newInternalPkgRev)
// 	return newInternalPkgRev, err
// }

// func (r packageCommon) amendApiPkgResourcesWithMetadata(apiPkgResources *api.PackageRevisionResources, internalPkgRev *internalapi.InternalPackageRevision) {
// 	apiPkgResources.Labels = internalPkgRev.Labels
// 	apiPkgResources.Annotations = internalPkgRev.Annotations
// }
