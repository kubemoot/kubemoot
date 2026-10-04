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
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

// TestAddToSchemeRegistersEveryCRDKind pins the scheme registration to the
// generated CRDs: every kind and list kind a CRD declares must resolve in the
// scheme, so a type whose init() registration is missing fails here.
func TestAddToSchemeRegistersEveryCRDKind(t *testing.T) {
	s := newTestScheme(t)
	files, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no CRD files found: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(data, &crd); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, kind := range []string{crd.Spec.Names.Kind, crd.Spec.Names.ListKind} {
			if !s.Recognizes(GroupVersion.WithKind(kind)) {
				t.Errorf("%s: kind %q is not registered in the scheme", crd.Name, kind)
			}
		}
	}
}

func TestAddToSchemeRegistersMetaKinds(t *testing.T) {
	s := newTestScheme(t)
	if !s.Recognizes(GroupVersion.WithKind("ListOptions")) {
		t.Error("metav1 ListOptions is not registered for the group version")
	}
	gvks, _, err := s.ObjectKinds(&Crew{})
	if err != nil || len(gvks) != 1 || gvks[0] != GroupVersion.WithKind("Crew") {
		t.Errorf("ObjectKinds(Crew) = %v, %v; want [%v]", gvks, err, GroupVersion.WithKind("Crew"))
	}
}

func TestAddToSchemeRejectsUnknownKind(t *testing.T) {
	s := newTestScheme(t)
	if s.Recognizes(GroupVersion.WithKind("NotAKubemootKind")) {
		t.Error("scheme recognizes a kind that was never registered")
	}
}
