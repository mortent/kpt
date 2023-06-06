// Copyright 2022 The kpt Authors
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

package draft

import (
	"context"
	"time"

	"github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	"github.com/GoogleContainerTools/kpt/porch/pkg/git/packagerevision"
	"github.com/GoogleContainerTools/kpt/porch/pkg/repository"
	"github.com/go-git/go-git/v5/plumbing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("draft")

type gitRepository interface {
	UpdateDraftResources(ctx context.Context, draft *GitPackageDraft, new *v1alpha1.PackageRevisionResources, change *v1alpha1.Task) error
	CloseDraft(ctx context.Context, d *GitPackageDraft) (*packagerevision.GitPackageRevision, error)
}

func NewGitPackageDraft(
	repo gitRepository,
	path string,
	revision string,
	workspace v1alpha1.WorkspaceName,
	lifecycle v1alpha1.PackageRevisionLifecycle,
	updated time.Time,
	base *plumbing.Reference,
	tasks []v1alpha1.Task,
	branch string,
	commit plumbing.Hash,
	tree plumbing.Hash,
) *GitPackageDraft {
	return &GitPackageDraft{
		parent:        repo,
		path:          path,
		revision:      revision,
		workspaceName: workspace,
		lifecycle:     lifecycle,
		updated:       updated,
		base:          base,  // Creating a new package
		tasks:         tasks, // Creating a new package
		branch:        branch,
		commit:        commit,
		tree:          tree,
	}
}

type GitPackageDraft struct {
	parent        gitRepository // repo is repo containing the package
	path          string        // the path to the package from the repo root
	revision      string
	workspaceName v1alpha1.WorkspaceName
	updated       time.Time
	tasks         []v1alpha1.Task

	// New value of the package revision lifecycle
	lifecycle v1alpha1.PackageRevisionLifecycle

	// ref to the base of the package update commit chain (used for conditional push)
	base *plumbing.Reference

	// name of the branch where the changes will be pushed
	branch string

	// Current HEAD of the package changes (commit sha)
	commit plumbing.Hash

	// Cached tree of the package itself, some descendent of commit.Tree()
	tree plumbing.Hash
}

var _ repository.PackageDraft = &GitPackageDraft{}

func (d *GitPackageDraft) Path() string {
	return d.path
}

func (d *GitPackageDraft) Commit() plumbing.Hash {
	return d.commit
}

func (d GitPackageDraft) Tree() plumbing.Hash {
	return d.tree
}

func (d *GitPackageDraft) Workspace() v1alpha1.WorkspaceName {
	return d.workspaceName
}

func (d *GitPackageDraft) Revision() string {
	return d.revision
}

func (d *GitPackageDraft) Base() *plumbing.Reference {
	return d.base
}

func (d *GitPackageDraft) Updated() time.Time {
	return d.updated
}

func (d *GitPackageDraft) Tasks() []v1alpha1.Task {
	return d.tasks
}

func (d *GitPackageDraft) SetTree(tree plumbing.Hash) {
	d.tree = tree
}

func (d *GitPackageDraft) SetCommit(commit plumbing.Hash) {
	d.commit = commit
}

func (d *GitPackageDraft) SetRevision(revision string) {
	d.revision = revision
}

func (d *GitPackageDraft) SetWorkspace(workspace v1alpha1.WorkspaceName) {
	d.workspaceName = workspace
}

func (d *GitPackageDraft) AppendTask(task v1alpha1.Task) {
	d.tasks = append(d.tasks, task)
}

func (d *GitPackageDraft) Lifecycle() v1alpha1.PackageRevisionLifecycle {
	return d.lifecycle
}

func (d *GitPackageDraft) UpdateResources(ctx context.Context, new *v1alpha1.PackageRevisionResources, change *v1alpha1.Task) error {
	ctx, span := tracer.Start(ctx, "gitPackageDraft::UpdateResources", trace.WithAttributes())
	defer span.End()

	return d.parent.UpdateDraftResources(ctx, d, new, change)
}

func (d *GitPackageDraft) UpdateLifecycle(ctx context.Context, new v1alpha1.PackageRevisionLifecycle) error {
	d.lifecycle = new
	return nil
}

// Finish round of updates.
func (d *GitPackageDraft) Close(ctx context.Context) (repository.PackageRevision, error) {
	ctx, span := tracer.Start(ctx, "gitPackageDraft::Close", trace.WithAttributes())
	defer span.End()

	return d.parent.CloseDraft(ctx, d)
}
