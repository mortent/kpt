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

package engine

import (
	"context"
	"fmt"

	Kptpkg "github.com/GoogleContainerTools/kpt/pkg/kptpkg"
	"github.com/GoogleContainerTools/kpt/pkg/printer"
	"github.com/GoogleContainerTools/kpt/pkg/printer/fake"
	api "github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	"github.com/GoogleContainerTools/kpt/porch/pkg/repository"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

type InitPackageMutation struct {
	Kptpkg.DefaultInitializer
	Name string
	Task *api.Task
}

var _ Mutation = &InitPackageMutation{}

func (m *InitPackageMutation) Apply(ctx context.Context, resources repository.PackageResources) (repository.PackageResources, *api.TaskResult, error) {
	ctx, span := tracer.Start(ctx, "initPackageMutation::Apply", trace.WithAttributes())
	defer span.End()

	fs := filesys.MakeFsInMemory()
	// virtual fs expected a rooted filesystem
	pkgPath := "/"

	if m.Task.Init.Subpackage != "" {
		pkgPath = "/" + m.Task.Init.Subpackage
	}
	if err := fs.Mkdir(pkgPath); err != nil {
		return repository.PackageResources{}, nil, err
	}
	err := m.Initialize(printer.WithContext(ctx, &fake.Printer{}), fs, Kptpkg.InitOptions{
		PkgPath:  pkgPath,
		PkgName:  m.Name,
		Desc:     m.Task.Init.Description,
		Keywords: m.Task.Init.Keywords,
		Site:     m.Task.Init.Site,
	})
	if err != nil {
		return repository.PackageResources{}, nil, fmt.Errorf("failed to initialize pkg %q: %w", m.Name, err)
	}

	result, err := readResources(fs)
	if err != nil {
		return repository.PackageResources{}, nil, err
	}

	return result, &api.TaskResult{Task: m.Task}, nil
}
