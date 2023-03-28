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

package packagerevisions

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/GoogleContainerTools/kpt/internal/fnruntime"
	"github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	configapi "github.com/GoogleContainerTools/kpt/porch/api/porchconfig/v1alpha1"
	"github.com/GoogleContainerTools/kpt/porch/pkg/apiserver"
	"github.com/GoogleContainerTools/kpt/porch/pkg/cache"
	"github.com/GoogleContainerTools/kpt/porch/pkg/engine"
	"github.com/GoogleContainerTools/kpt/porch/pkg/registry/porch"
	"github.com/GoogleContainerTools/kpt/porch/pkg/repository"
	"google.golang.org/api/option"
	"google.golang.org/api/sts/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	cacheDirectory        = ""
	functionRunnerAddress = "192.168.8.202:9445"
	defaultImagePrefix    = "gcr.io/kpt-fn/"
)

type Options struct {
}

func (o *Options) InitDefaults() {
}

func (o *Options) BindFlags(prefix string, flags *flag.FlagSet) {
}

func NewPackageRevisionReconciler() *PackageRevisionReconciler {
	return &PackageRevisionReconciler{}
}

// PackageRevisionReconciler reconciles a PackageRevision object
type PackageRevisionReconciler struct {
	Options

	client.Client

	engine engine.CaDEngine
}

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.8.0 rbac:roleName=porch-controllers-packagerevisions webhook paths="." output:rbac:artifacts:config=../../../config/rbac

//+kubebuilder:rbac:groups=porch.kpt.dev,resources=packagerevisions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=porch.kpt.dev,resources=packagerevisions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=porch.kpt.dev,resources=packagerevisions/finalizers,verbs=update
//+kubebuilder:rbac:groups=porch.kpt.dev,resources=repositories,verbs=get;list;watch

func (r *PackageRevisionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pkgRev v1alpha1.PackageRevision
	if err := r.Get(ctx, req.NamespacedName, &pkgRev); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.Infof("reconciling %s", req.NamespacedName)

	myFinalizerName := "porch.kpt.dev/packagerevisions"
	if pkgRev.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !controllerutil.ContainsFinalizer(&pkgRev, myFinalizerName) {
			controllerutil.AddFinalizer(&pkgRev, myFinalizerName)
			if err := r.Update(ctx, &pkgRev); err != nil {
				return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
			}
		}
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(&pkgRev, myFinalizerName) {
			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(&pkgRev, myFinalizerName)
			if err := r.Update(ctx, &pkgRev); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update %s after delete finalizer: %w", req.Name, err)
			}
		}
		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}
	klog.Infof("foo!")

	var repoObj configapi.Repository
	nn := types.NamespacedName{
		Name:      pkgRev.Spec.RepositoryName,
		Namespace: pkgRev.Namespace,
	}
	if err := r.Get(ctx, nn, &repoObj); err != nil {
		if apierrors.IsNotFound(err) {
			// TODO: We need to figure out what to do in this case. We probably need this controller
			// to clean up packagerevisions when a repository is deleted.
		}
		return ctrl.Result{}, err
	}

	repo, err := r.engine.OpenRepository(ctx, &repoObj)
	if err != nil {
		return ctrl.Result{}, err
	}

	repoPkgRevs, err := repo.ListPackageRevisions(ctx, repository.ListPackageRevisionFilter{
		KubeObjectName: pkgRev.Name,
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(repoPkgRevs) == 0 {
		klog.Infof("no repo revision found for %s", pkgRev.Name)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageRevisionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := v1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}
	if err := configapi.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	r.Client = mgr.GetClient()
	cfg := mgr.GetConfig()

	coreV1Client, err := corev1client.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("error building corev1 client: %w", err)
	}

	stsClient, err := sts.NewService(context.Background(), option.WithoutAuthentication())
	if err != nil {
		return fmt.Errorf("failed to build sts client: %w", err)
	}

	resolverChain := []porch.Resolver{
		porch.NewBasicAuthResolver(),
		porch.NewGcloudWIResolver(coreV1Client, stsClient),
	}

	credentialResolver := porch.NewCredentialResolver(r.Client, resolverChain)
	referenceResolver := porch.NewReferenceResolver(r.Client)
	userInfoProvider := &porch.ApiserverUserInfoProvider{}

	cacheDir := cacheDirectory
	if cacheDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
			klog.Warningf("Cannot find user cache directory, using temporary directory %q", cache)
		}
		cacheDir = cache + "/porch"
	}
	klog.Infof("CacheDir: %s", cacheDir)
	cache := cache.NewCache(cacheDir, cache.CacheOptions{
		CredentialResolver: credentialResolver,
		UserInfoProvider:   userInfoProvider,
		// MetadataStore:      metadataStore,
		// ObjectNotifier:     watcherMgr,
	})

	runnerOptionsResolver := func(namespace string) fnruntime.RunnerOptions {
		runnerOptions := fnruntime.RunnerOptions{}
		runnerOptions.InitDefaults()
		r := &apiserver.KubeFunctionResolver{
			Client:             r.Client,
			DefaultImagePrefix: defaultImagePrefix,
			Namespace:          namespace,
		}
		runnerOptions.ResolveToImage = r.ResolveToImagePorch

		return runnerOptions
	}

	cad, err := engine.NewCaDEngine(
		engine.WithCache(cache),
		// The order of registering the function runtimes matters here. When
		// evaluating a function, the runtimes will be tried in the same
		// order as they are registered.
		engine.WithBuiltinFunctionRuntime(),
		engine.WithGRPCFunctionRuntime(functionRunnerAddress),
		engine.WithCredentialResolver(credentialResolver),
		engine.WithRunnerOptionsResolver(runnerOptionsResolver),
		engine.WithReferenceResolver(referenceResolver),
		engine.WithUserInfoProvider(userInfoProvider),
		// engine.WithMetadataStore(metadataStore),
		// engine.WithWatcherManager(watcherMgr),
	)
	if err != nil {
		return err
	}
	r.engine = cad

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PackageRevision{}).
		Complete(r)
}
