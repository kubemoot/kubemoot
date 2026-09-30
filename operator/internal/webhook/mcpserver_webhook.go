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

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-kubemoot-ai-v1alpha1-mcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubemoot.ai,resources=mcpservers,verbs=create;update,versions=v1alpha1,name=vmcpserver.kubemoot.ai,admissionReviewVersions=v1

// MCPServerValidator validates MCPServer resources.
type MCPServerValidator struct{}

func (v *MCPServerValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	server, ok := obj.(*aiv1alpha1.MCPServer)
	if !ok {
		return nil, fmt.Errorf("expected MCPServer, got %T", obj)
	}
	return validateMCPServer(server)
}

func (v *MCPServerValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	server, ok := newObj.(*aiv1alpha1.MCPServer)
	if !ok {
		return nil, fmt.Errorf("expected MCPServer, got %T", newObj)
	}
	return validateMCPServer(server)
}

func (v *MCPServerValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateMCPServer(server *aiv1alpha1.MCPServer) (admission.Warnings, error) {
	hasImage := server.Spec.Image != ""
	hasExternal := server.Spec.ExternalEndpoint != ""

	if hasImage && hasExternal {
		return nil, fmt.Errorf("image and externalEndpoint are mutually exclusive, set only one")
	}
	if !hasImage && !hasExternal {
		return nil, fmt.Errorf("one of image or externalEndpoint must be set")
	}

	if server.Spec.Transport == aiv1alpha1.TransportStdio && hasExternal {
		return nil, fmt.Errorf("transport stdio requires a managed pod (image), not an externalEndpoint")
	}

	return nil, nil
}
