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

package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	configapi "github.com/GoogleContainerTools/kpt/porch/api/porchconfig/v1alpha1"
	internalapi "github.com/GoogleContainerTools/kpt/porch/internal/api/porchinternal/v1alpha1"
	"github.com/GoogleContainerTools/kpt/porch/pkg/meta"
	"github.com/GoogleContainerTools/kpt/porch/pkg/repository"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"
)

var tracer = otel.Tracer("cache")

// We take advantage of the cache having a global view of all the packages
// in a repository and compute the latest package revision in the cache
// rather than add another level of caching in the repositories themselves.
// This also reuses the revision comparison code and ensures same behavior
// between Git and OCI.

var _ repository.Repository = &cachedRepository{}
var _ repository.FunctionRepository = &cachedRepository{}

type cachedRepository struct {
	id       string
	repoSpec *configapi.Repository
	repo     repository.Repository
	cancel   context.CancelFunc

	mutex                  sync.Mutex
	cachedPackageRevisions map[repository.PackageRevisionKey]*cachedPackageRevision
	cachedPackages         map[repository.PackageKey]*cachedPackage

	// TODO: Currently we support repositories with homogenous content (only packages xor functions). Model this more optimally?
	cachedFunctions []repository.Function
	// Error encountered on repository refresh by the refresh goroutine.
	// This is returned back by the cache to the background goroutine when it calls periodicall to resync repositories.
	refreshRevisionsError error
	refreshPkgsError      error

	objectCache *objectCache

	metadataStore meta.MetadataStore
}

func newRepository(id string, repoSpec *configapi.Repository, repo repository.Repository, objectCache *objectCache, metadataStore meta.MetadataStore) *cachedRepository {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cachedRepository{
		id:            id,
		repoSpec:      repoSpec,
		repo:          repo,
		cancel:        cancel,
		objectCache:   objectCache,
		metadataStore: metadataStore,
	}

	// TODO: Should we fetch the packages here?

	go r.pollForever(ctx)

	return r
}

func (r *cachedRepository) ListPackageRevisions(ctx context.Context, filter repository.ListPackageRevisionFilter) ([]repository.PackageRevision, error) {
	packages, err := r.getPackageRevisions(ctx, filter, false)
	if err != nil {
		return nil, err
	}

	return packages, nil
}

func (r *cachedRepository) ListFunctions(ctx context.Context) ([]repository.Function, error) {
	functions, err := r.getFunctions(ctx, false)
	if err != nil {
		return nil, err
	}
	return functions, nil
}

func (r *cachedRepository) getRefreshError() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// TODO: This should also check r.refreshPkgsError when
	//   the package resource is fully supported.

	return r.refreshRevisionsError
}

func (r *cachedRepository) getPackageRevisions(ctx context.Context, filter repository.ListPackageRevisionFilter, forceRefresh bool) ([]repository.PackageRevision, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	_, packageRevisions, err := r.getCachedPackages(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}

	return toPackageRevisionSlice(packageRevisions, filter), nil
}

func (r *cachedRepository) getPackages(ctx context.Context, filter repository.ListPackageFilter, forceRefresh bool) ([]repository.Package, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	packages, _, err := r.getCachedPackages(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}

	return toPackageSlice(packages, filter), nil
}

// getCachedPackages returns cachedPackages; fetching it if not cached or if forceRefresh.
// mutex must be held.
func (r *cachedRepository) getCachedPackages(ctx context.Context, forceRefresh bool) (map[repository.PackageKey]*cachedPackage, map[repository.PackageRevisionKey]*cachedPackageRevision, error) {
	// must hold mutex
	packages := r.cachedPackages
	packageRevisions := r.cachedPackageRevisions
	err := r.refreshRevisionsError

	if forceRefresh {
		packages = nil
		packageRevisions = nil
	}

	if packages == nil {
		packages, packageRevisions, err = r.refreshAllCachedPackages(ctx)
	}

	return packages, packageRevisions, err
}

func (r *cachedRepository) getFunctions(ctx context.Context, force bool) ([]repository.Function, error) {
	var functions []repository.Function

	if !force {
		r.mutex.Lock()
		functions = r.cachedFunctions
		r.mutex.Unlock()
	}

	if functions == nil {
		fr, ok := (r.repo).(repository.FunctionRepository)
		if !ok {
			return []repository.Function{}, nil
		}

		if f, err := fr.ListFunctions(ctx); err != nil {
			return nil, err
		} else {
			functions = f
		}

		r.mutex.Lock()
		r.cachedFunctions = functions
		r.mutex.Unlock()
	}

	return functions, nil
}

func (r *cachedRepository) CreatePackageRevision(ctx context.Context, obj *v1alpha1.PackageRevision) (repository.PackageDraft, error) {
	created, err := r.repo.CreatePackageRevision(ctx, obj)
	if err != nil {
		return nil, err
	}

	return &cachedDraft{
		PackageDraft: created,
		cache:        r,
	}, nil
}

func (r *cachedRepository) UpdatePackageRevision(ctx context.Context, old repository.PackageRevision) (repository.PackageDraft, error) {
	// Unwrap
	unwrapped := old.(*cachedPackageRevision).PackageRevision
	created, err := r.repo.UpdatePackageRevision(ctx, unwrapped)
	if err != nil {
		return nil, err
	}

	return &cachedDraft{
		PackageDraft: created,
		cache:        r,
	}, nil
}

func (r *cachedRepository) update(ctx context.Context, updated repository.PackageRevision) (*cachedPackageRevision, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// TODO: Technically we only need this package, not all packages
	if _, _, err := r.getCachedPackages(ctx, false); err != nil {
		klog.Warningf("failed to get cached packages: %v", err)
		// TODO: Invalidate all watches? We're dropping an add/update event
		return nil, err
	}

	k := updated.Key()
	// previous := r.cachedPackageRevisions[k]

	cached := &cachedPackageRevision{
		PackageRevision: updated,
	}
	r.cachedPackageRevisions[k] = cached

	// Recompute latest package revisions.
	// TODO: Just updated package?
	identifyLatestRevisions(r.cachedPackageRevisions)

	// TODO: Update the latest revisions for the r.cachedPackages
	return cached, nil
}

func (r *cachedRepository) DeletePackageRevision(ctx context.Context, old repository.PackageRevision) error {
	// Unwrap
	unwrapped := old.(*cachedPackageRevision).PackageRevision
	if err := r.repo.DeletePackageRevision(ctx, unwrapped); err != nil {
		return err
	}

	r.mutex.Lock()
	if r.cachedPackages != nil {
		k := old.Key()
		// previous := r.cachedPackages[k]
		delete(r.cachedPackageRevisions, k)

		// Recompute latest package revisions.
		// TODO: Only for affected object / key?
		identifyLatestRevisions(r.cachedPackageRevisions)
	}

	r.mutex.Unlock()

	return nil
}

func (r *cachedRepository) ListPackages(ctx context.Context, filter repository.ListPackageFilter) ([]repository.Package, error) {
	packages, err := r.getPackages(ctx, filter, false)
	if err != nil {
		return nil, err
	}

	return packages, nil
}

func (r *cachedRepository) CreatePackage(ctx context.Context, obj *v1alpha1.Package) (repository.Package, error) {
	klog.Infoln("cachedRepository::CreatePackage")
	return r.repo.CreatePackage(ctx, obj)
}

func (r *cachedRepository) DeletePackage(ctx context.Context, old repository.Package) error {
	// Unwrap
	unwrapped := old.(*cachedPackage).Package
	if err := r.repo.DeletePackage(ctx, unwrapped); err != nil {
		return err
	}

	// TODO: Do something more efficient than a full cache flush
	r.flush()

	return nil
}

func (r *cachedRepository) Close() error {
	r.cancel()
	return nil
}

// pollForever will continue polling until signal channel is closed or ctx is done.
func (r *cachedRepository) pollForever(ctx context.Context) {
	// Poll immediately so we don't have to wait for the ticker to trigger the first
	// sync of the repo.
	r.pollOnce(ctx)

	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case <-ticker.C:
			r.pollOnce(ctx)

		case <-ctx.Done():
			klog.V(2).Infof("exiting repository poller, because context is done: %v", ctx.Err())
			return
		}
	}
}

func (r *cachedRepository) pollOnce(ctx context.Context) {
	klog.Infof("background-refreshing repo %q", r.id)
	ctx, span := tracer.Start(ctx, "Repository::pollOnce", trace.WithAttributes())
	defer span.End()

	if _, err := r.getPackageRevisions(ctx, repository.ListPackageRevisionFilter{}, true); err != nil {
		klog.Warningf("error polling repo packages %s: %v", r.id, err)
	}
	// TODO: Uncomment when package resources are fully supported
	//if _, err := r.getPackages(ctx, repository.ListPackageRevisionFilter{}, true); err != nil {
	//	klog.Warningf("error polling repo packages %s: %v", r.id, err)
	//}
	if _, err := r.getFunctions(ctx, true); err != nil {
		klog.Warningf("error polling repo functions %s: %v", r.id, err)
	}
}

func (r *cachedRepository) flush() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.cachedPackageRevisions = nil
	r.cachedPackages = nil
}

// refreshAllCachedPackages updates the cached map for this repository with all the newPackages,
// it also triggers notifications for all package changes.
// mutex must be held.
func (r *cachedRepository) refreshAllCachedPackages(ctx context.Context) (map[repository.PackageKey]*cachedPackage, map[repository.PackageRevisionKey]*cachedPackageRevision, error) {
	// We probably want to do something here.
	// TODO: Avoid simultaneous fetches?
	// TODO: Push-down partial refresh?

	// TODO: Can we avoid holding the lock for the ListPackageRevisions / identifyLatestRevisions section?
	newPackageRevisions, err := r.repo.ListPackageRevisions(ctx, repository.ListPackageRevisionFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("error listing packages: %w", err)
	}

	newPackageRevisionMap := make(map[repository.PackageRevisionKey]*cachedPackageRevision, len(newPackageRevisions))
	for _, newPackage := range newPackageRevisions {
		k := newPackage.Key()
		if newPackageRevisionMap[k] != nil {
			klog.Warningf("found duplicate packages with key %v", k)
		}

		// We shouldn't need to look up these every time. We can pick them from the
		// previous list and only fetch if they don't exist there.
		internalPkgRev, err := r.metadataStore.Get(ctx, newPackage.KubeObjectName(), newPackage.KubeObjectNamespace())
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		if apierrors.IsNotFound(err) {
			klog.Infof("registring new package %s %s", newPackage.KubeObjectName(), newPackage.KubeObjectNamespace())
			if internalPkgRev, err = r.metadataStore.Create(ctx, newPackage.KubeObjectName(), newPackage.KubeObjectNamespace(),
				map[string]string{}, map[string]string{}, r.repoSpec); err != nil {
				return nil, nil, err
			}
		}

		newPackageRevisionMap[k] = &cachedPackageRevision{
			PackageRevision:  newPackage,
			internalPkgRev:   internalPkgRev,
			isLatestRevision: false,
		}
	}

	identifyLatestRevisions(newPackageRevisionMap)

	newPackageMap := make(map[repository.PackageKey]*cachedPackage)

	for _, newPackageRevision := range newPackageRevisionMap {
		if !newPackageRevision.isLatestRevision {
			continue
		}
		// TODO: Build package?
		// newPackage := &cachedPackage{
		// }
		// newPackageMap[newPackage.Key()] = newPackage
	}

	oldPackageRevisions := r.cachedPackageRevisions
	r.cachedPackageRevisions = newPackageRevisionMap
	r.cachedPackages = newPackageMap

	// This stuff seems like it is at the wrong place. I would expect
	// all these mutating operations to go through the Engine. However,
	// that is not the case now.
	for k, newPackage := range r.cachedPackageRevisions {
		oldPackage := oldPackageRevisions[k]
		if oldPackage == nil {
			r.objectCache.notifyPackageRevisionChange(watch.Added, newPackage)
		} else {
			// TODO: only if changed
			klog.Warningf("over-notifying of package updates (even on unchanged packages)")
			r.objectCache.notifyPackageRevisionChange(watch.Modified, newPackage)
		}
	}

	for k, oldPackage := range oldPackageRevisions {
		if newPackageRevisionMap[k] == nil {
			klog.Infof("removing old package %s %s", oldPackage.KubeObjectName(), oldPackage.KubeObjectNamespace())
			if _, err := r.metadataStore.Delete(ctx, oldPackage.KubeObjectName(), oldPackage.KubeObjectNamespace()); err != nil {
				return nil, nil, err
			}
			r.objectCache.notifyPackageRevisionChange(watch.Deleted, oldPackage)
		}
	}

	return newPackageMap, newPackageRevisionMap, nil
}

// TODO: Fix this.
func UpdateCachedPkgRev(repoPkgRev repository.PackageRevision, internalPkgRev *internalapi.InternalPackageRevision) {
	cachedPkgRev, ok := repoPkgRev.(*cachedPackageRevision)
	if !ok {
		return
	}
	cachedPkgRev.internalPkgRev = internalPkgRev
}
