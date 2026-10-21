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

package v1alpha1

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func loadCRD(t *testing.T, path ...string) apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	return crd
}

// An EmbeddingModel names its model and no server; the chart's copy of the CRD
// must say the same as the generated one.
func TestEmbeddingModelCRDDoesNotRequireAServer(t *testing.T) {
	for _, dir := range [][]string{{"..", "..", "config", "crd", "bases"}, {"..", "..", "chart", "kubemoot-operator", "crds"}} {
		crd := loadCRD(t, append(dir, "kubemoot.ai_embeddingmodels.yaml")...)
		spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
		if !slices.Equal(spec.Required, []string{"model"}) {
			t.Errorf("%v: required spec fields = %v, want only model", dir, spec.Required)
		}
		if _, ok := spec.Properties["serverRef"]; !ok {
			t.Errorf("%v: spec.serverRef must remain, optional, for crew-local pinning", dir)
		}
		status := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
		if _, ok := status.Properties["boundServer"]; !ok {
			t.Errorf("%v: status.boundServer is missing", dir)
		}
	}
}

func TestModelServerCRDListsEmbeddingModels(t *testing.T) {
	crd := loadCRD(t, "..", "..", "config", "crd", "bases", "kubemoot.ai_modelservers.yaml")
	status := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
	if _, ok := status.Properties["embeddingModels"]; !ok {
		t.Error("ModelServer status.embeddingModels is missing")
	}
}

func TestMCPGatewayToolIndexDoesNotRequireAnEmbeddingEndpoint(t *testing.T) {
	crd := loadCRD(t, "..", "..", "config", "crd", "bases", "kubemoot.ai_mcpgateways.yaml")
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	embedding := spec.Properties["toolIndex"].Properties["embeddingModel"]
	if slices.Contains(embedding.Required, "endpoint") {
		t.Errorf("toolIndex.embeddingModel requires %v; the endpoint comes from the EmbeddingModel's bound server", embedding.Required)
	}
}
