// Copyright 2021 Google LLC
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

package resolver

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLiveErrorResolver(t *testing.T) {
	testCases := map[string]struct {
		err      error
		expected string
	}{}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			res, ok := (&liveErrorResolver{}).Resolve(tc.err)
			if !ok {
				t.Error("expected error to be resolved, but it wasn't")
			}
			assert.Equal(t, strings.TrimSpace(tc.expected), strings.TrimSpace(res.Message))
		})
	}
}
