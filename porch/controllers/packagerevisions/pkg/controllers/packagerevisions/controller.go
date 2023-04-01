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

	"github.com/GoogleContainerTools/kpt/internal/builtins"
	"github.com/GoogleContainerTools/kpt/internal/fnruntime"
	"github.com/GoogleContainerTools/kpt/pkg/fn"
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

	engine                engine.CaDEngine
	referenceResolver     engine.ReferenceResolver
	runnerOptionsResolver func(namespace string) fnruntime.RunnerOptions
	runtime               fn.FunctionRuntime
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
		klog.Info("Creating new PackageRevision")
		draft, err := repo.CreatePackageRevision(ctx, &pkgRev)
		if err != nil {
			return ctrl.Result{}, err
		}

		packageConfig, err := engine.BuildPackageConfig(ctx, &pkgRev, nil)
		if err != nil {
			return ctrl.Result{}, err
		}

		if err := r.applyTasks(ctx, draft, &repoObj, &pkgRev, packageConfig); err != nil {
			return ctrl.Result{}, err
		}

		if err := draft.UpdateLifecycle(ctx, pkgRev.Spec.Lifecycle); err != nil {
			return ctrl.Result{}, err
		}

		// Updates are done.
		_, err = draft.Close(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	repoPkgRev := repoPkgRevs[0]
	oldPkgRev, err := repoPkgRev.GetPackageRevision(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	var mutations []engine.Mutation
	if len(oldPkgRev.Spec.Tasks) > len(pkgRev.Spec.Tasks) {
		return ctrl.Result{}, fmt.Errorf("removing tasks is not yet supported")
	}
	for i := range oldPkgRev.Spec.Tasks {
		oldTask := &oldPkgRev.Spec.Tasks[i]
		newTask := &pkgRev.Spec.Tasks[i]
		if oldTask.Type != newTask.Type {
			return ctrl.Result{}, fmt.Errorf("changing task types is not yet supported")
		}
	}
	if len(pkgRev.Spec.Tasks) > len(oldPkgRev.Spec.Tasks) {
		if len(pkgRev.Spec.Tasks) > len(oldPkgRev.Spec.Tasks)+1 {
			return ctrl.Result{}, fmt.Errorf("can only append one task at a time")
		}

		newTask := pkgRev.Spec.Tasks[len(pkgRev.Spec.Tasks)-1]
		if newTask.Type != v1alpha1.TaskTypeUpdate {
			return ctrl.Result{}, fmt.Errorf("appended task is type %q, must be type %q", newTask.Type, v1alpha1.TaskTypeUpdate)
		}
		if newTask.Update == nil {
			return ctrl.Result{}, fmt.Errorf("update not set for updateTask of type %q", newTask.Type)
		}

		cloneTask := engine.FindCloneTask(oldPkgRev)
		if cloneTask == nil {
			return ctrl.Result{}, fmt.Errorf("upstream source not found for package rev %q; only cloned packages can be updated", oldPkgRev.Spec.PackageName)
		}

		mutation := &engine.UpdatePackageMutation{
			CloneTask:         cloneTask,
			UpdateTask:        &newTask,
			RepoOpener:        r.engine,
			ReferenceResolver: r.referenceResolver,
			Namespace:         repoObj.GetNamespace(),
			PkgName:           pkgRev.GetName(),
		}
		mutations = append(mutations, mutation)
	}

	// Re-render if we are making changes.
	mutations = r.conditionalAddRender(&pkgRev, mutations)

	draft, err := repo.UpdatePackageRevision(ctx, repoPkgRev)
	if err != nil {
		return ctrl.Result{}, err
	}

	// If any of the fields in the API that are projections from the Kptfile
	// must be updated in the Kptfile as well.
	kfPatchTask, created, err := engine.CreateKptfilePatchTask(ctx, repoPkgRev, &pkgRev)
	if err != nil {
		return ctrl.Result{}, err
	}
	if created {
		kfPatchMutation, err := engine.BuildPatchMutation(ctx, kfPatchTask)
		if err != nil {
			return ctrl.Result{}, err
		}
		mutations = append(mutations, kfPatchMutation)
	}

	// Re-render if we are making changes.
	mutations = r.conditionalAddRender(&pkgRev, mutations)

	// TODO: Handle the case if alongside lifecycle change, tasks are changed too.
	// Update package contents only if the package is in draft state
	if oldPkgRev.Spec.Lifecycle == v1alpha1.PackageRevisionLifecycleDraft {
		apiResources, err := repoPkgRev.GetResources(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("cannot get package resources: %w", err)
		}
		resources := repository.PackageResources{
			Contents: apiResources.Spec.Resources,
		}

		if _, _, err := engine.ApplyResourceMutations(ctx, draft, resources, mutations); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := draft.UpdateLifecycle(ctx, pkgRev.Spec.Lifecycle); err != nil {
		return ctrl.Result{}, err
	}

	// Updates are done.
	repoPkgRev, err = draft.Close(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *PackageRevisionReconciler) applyTasks(ctx context.Context, draft repository.PackageDraft, repositoryObj *configapi.Repository, obj *v1alpha1.PackageRevision, packageConfig *builtins.PackageConfig) error {
	var mutations []engine.Mutation

	// Unless first task is Init or Clone, insert Init to create an empty package.
	tasks := obj.Spec.Tasks
	if len(tasks) == 0 || !engine.TaskTypeOneOf(tasks[0].Type, v1alpha1.TaskTypeInit, v1alpha1.TaskTypeClone, v1alpha1.TaskTypeEdit) {
		mutations = append(mutations, &engine.InitPackageMutation{
			Name: obj.Spec.PackageName,
			Task: &v1alpha1.Task{
				Init: &v1alpha1.PackageInitTaskSpec{
					Subpackage:  "",
					Description: fmt.Sprintf("%s description", obj.Spec.PackageName),
				},
			},
		})
	}

	for i := range tasks {
		task := &tasks[i]
		mutation, err := r.mapTaskToMutation(ctx, obj, task, repositoryObj.Spec.Deployment, packageConfig)
		if err != nil {
			return err
		}
		mutations = append(mutations, mutation)
	}

	// Render package after creation.
	mutations = r.conditionalAddRender(obj, mutations)

	baseResources := repository.PackageResources{}
	if _, _, err := engine.ApplyResourceMutations(ctx, draft, baseResources, mutations); err != nil {
		return err
	}

	return nil
}

func (r *PackageRevisionReconciler) mapTaskToMutation(ctx context.Context, obj *v1alpha1.PackageRevision, task *v1alpha1.Task, isDeployment bool, packageConfig *builtins.PackageConfig) (engine.Mutation, error) {
	switch task.Type {
	case v1alpha1.TaskTypeInit:
		if task.Init == nil {
			return nil, fmt.Errorf("init not set for task of type %q", task.Type)
		}
		return &engine.InitPackageMutation{
			Name: obj.Spec.PackageName,
			Task: task,
		}, nil
	case v1alpha1.TaskTypeClone:
		if task.Clone == nil {
			return nil, fmt.Errorf("clone not set for task of type %q", task.Type)
		}
		return &engine.ClonePackageMutation{
			Task:               task,
			Namespace:          obj.Namespace,
			Name:               obj.Spec.PackageName,
			IsDeployment:       isDeployment,
			RepoOpener:         r.engine,
			CredentialResolver: nil,
			ReferenceResolver:  r.referenceResolver,
			PackageConfig:      packageConfig,
		}, nil

	case v1alpha1.TaskTypeUpdate:
		if task.Update == nil {
			return nil, fmt.Errorf("update not set for task of type %q", task.Type)
		}
		cloneTask := engine.FindCloneTask(obj)
		if cloneTask == nil {
			return nil, fmt.Errorf("upstream source not found for package rev %q; only cloned packages can be updated", obj.Spec.PackageName)
		}
		return &engine.UpdatePackageMutation{
			CloneTask:         cloneTask,
			UpdateTask:        task,
			Namespace:         obj.Namespace,
			RepoOpener:        r.engine,
			ReferenceResolver: r.referenceResolver,
			PkgName:           obj.Spec.PackageName,
		}, nil

	case v1alpha1.TaskTypePatch:
		return engine.BuildPatchMutation(ctx, task)

	case v1alpha1.TaskTypeEdit:
		if task.Edit == nil {
			return nil, fmt.Errorf("edit not set for task of type %q", task.Type)
		}
		return &engine.EditPackageMutation{
			Task:              task,
			Namespace:         obj.Namespace,
			PackageName:       obj.Spec.PackageName,
			RepositoryName:    obj.Spec.RepositoryName,
			RepoOpener:        r.engine,
			ReferenceResolver: r.referenceResolver,
		}, nil

	case v1alpha1.TaskTypeEval:
		if task.Eval == nil {
			return nil, fmt.Errorf("eval not set for task of type %q", task.Type)
		}
		// TODO: We should find a different way to do this. Probably a separate
		// task for render.
		if task.Eval.Image == "render" {
			runnerOptions := r.runnerOptionsResolver(obj.Namespace)
			return &engine.RenderPackageMutation{
				RunnerOptions: runnerOptions,
				Runtime:       r.runtime,
			}, nil
		} else {
			runnerOptions := r.runnerOptionsResolver(obj.Namespace)
			return &engine.EvalFunctionMutation{
				RunnerOptions: runnerOptions,
				Runtime:       r.runtime,
				Task:          task,
			}, nil
		}

	default:
		return nil, fmt.Errorf("task of type %q not supported", task.Type)
	}
}

// conditionalAddRender adds a render mutation to the end of the mutations slice if the last
// entry is not already a render mutation.
func (r *PackageRevisionReconciler) conditionalAddRender(subject client.Object, mutations []engine.Mutation) []engine.Mutation {
	if len(mutations) == 0 || engine.IsRenderMutation(mutations[len(mutations)-1]) {
		return mutations
	}

	runnerOptions := r.runnerOptionsResolver(subject.GetNamespace())

	return append(mutations, &engine.RenderPackageMutation{
		RunnerOptions: runnerOptions,
		Runtime:       r.runtime,
	})
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

	builtinRuntime := engine.NewBuiltinRuntime()
	grpcRuntime, err := engine.NewGRPCFunctionRuntime(functionRunnerAddress)

	runtime := fn.NewMultiRuntime([]fn.FunctionRuntime{
		builtinRuntime,
		grpcRuntime,
	})

	cad, err := engine.NewCaDEngine(
		engine.WithCache(cache),
		// The order of registering the function runtimes matters here. When
		// evaluating a function, the runtimes will be tried in the same
		// order as they are registered.
		engine.WithFunctionRuntime(runtime),
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
	r.referenceResolver = referenceResolver
	r.runnerOptionsResolver = runnerOptionsResolver
	r.runtime = runtime

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PackageRevision{}).
		Complete(r)
}
