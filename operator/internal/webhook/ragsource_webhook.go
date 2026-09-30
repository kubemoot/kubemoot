/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-kubemoot-ai-v1alpha1-ragsource,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubemoot.ai,resources=ragsources,verbs=create;update,versions=v1alpha1,name=vragsource.kubemoot.ai,admissionReviewVersions=v1

// RAGSourceValidator validates RAGSource resources.
type RAGSourceValidator struct{}

func (v *RAGSourceValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rs, ok := obj.(*aiv1alpha1.RAGSource)
	if !ok {
		return nil, fmt.Errorf("expected RAGSource, got %T", obj)
	}
	return validateRAGSource(rs)
}

func (v *RAGSourceValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rs, ok := newObj.(*aiv1alpha1.RAGSource)
	if !ok {
		return nil, fmt.Errorf("expected RAGSource, got %T", newObj)
	}
	return validateRAGSource(rs)
}

func (v *RAGSourceValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// globChars are characters that indicate a glob pattern.
const globChars = "*?[{"

func validateRAGSource(rs *aiv1alpha1.RAGSource) (admission.Warnings, error) {
	src := rs.Spec.Source

	if err := validateSourceTypePresence(src); err != nil {
		return nil, err
	}

	if err := validateGitPaths(src); err != nil {
		return nil, err
	}

	return nil, nil
}

// sourceTypeCheck describes, for one source type, whether its sub-spec is
// present and the error to return when it is not.
type sourceTypeCheck struct {
	present func(aiv1alpha1.SourceConfig) bool
	err     string
}

// sourceTypeChecks maps each validated source type to its presence check.
var sourceTypeChecks = map[aiv1alpha1.RAGSourceType]sourceTypeCheck{
	aiv1alpha1.RAGSourceTypeGit: {
		present: func(s aiv1alpha1.SourceConfig) bool { return s.Git != nil },
		err:     "source.type is git but source.git is not set",
	},
	aiv1alpha1.RAGSourceTypeS3: {
		present: func(s aiv1alpha1.SourceConfig) bool { return s.S3 != nil },
		err:     "source.type is s3 but source.s3 is not set",
	},
	aiv1alpha1.RAGSourceTypeURL: {
		present: func(s aiv1alpha1.SourceConfig) bool { return s.URL != nil },
		err:     "source.type is url but source.url is not set",
	},
	aiv1alpha1.RAGSourceTypeMCPRegistry: {
		present: func(s aiv1alpha1.SourceConfig) bool { return s.MCPRegistry != nil },
		err:     "source.type is mcp-registry but source.mcpRegistry is not set",
	},
	aiv1alpha1.RAGSourceTypeDocument: {
		present: func(s aiv1alpha1.SourceConfig) bool { return s.Document != nil },
		err:     "source.type is document but source.document is not set",
	},
}

// validateSourceTypePresence verifies the sub-spec matching source.type is set.
func validateSourceTypePresence(src aiv1alpha1.SourceConfig) error {
	check, ok := sourceTypeChecks[src.Type]
	if !ok {
		return nil
	}
	if !check.present(src) {
		return fmt.Errorf("%s", check.err)
	}
	return nil
}

// validateGitPaths rejects glob patterns in source.git.paths.
func validateGitPaths(src aiv1alpha1.SourceConfig) error {
	if src.Git == nil {
		return nil
	}
	for i, path := range src.Git.Paths {
		if strings.ContainsAny(path, globChars) {
			return fmt.Errorf("source.git.paths[%d]: glob patterns are not supported (found %q), use plain directory paths", i, path)
		}
	}
	return nil
}
