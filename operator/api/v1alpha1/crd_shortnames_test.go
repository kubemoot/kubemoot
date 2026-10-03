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
	"sigs.k8s.io/yaml"
)

// builtinShortNames are the short names of built-in Kubernetes resources. A CRD that
// reuses one makes kubectl warn "short name is ambiguous" on every use of it, for
// example every `kubectl get ns` on a cluster with Kubemoot installed.
var builtinShortNames = map[string]string{
	"cm": "configmaps", "crd": "customresourcedefinitions", "crds": "customresourcedefinitions",
	"cs": "componentstatuses", "csr": "certificatesigningrequests", "cj": "cronjobs",
	"deploy": "deployments", "ds": "daemonsets", "ep": "endpoints", "ev": "events",
	"hpa": "horizontalpodautoscalers", "ing": "ingresses", "limits": "limitranges",
	"netpol": "networkpolicies", "no": "nodes", "ns": "namespaces",
	"pc": "priorityclasses", "pdb": "poddisruptionbudgets", "po": "pods",
	"pv": "persistentvolumes", "pvc": "persistentvolumeclaims", "quota": "resourcequotas",
	"rc": "replicationcontrollers", "rs": "replicasets", "sa": "serviceaccounts",
	"sc": "storageclasses", "sts": "statefulsets", "svc": "services",
}

func TestCRDShortNamesDoNotShadowBuiltins(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no CRD files found: %v", err)
	}
	seen := map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(data, &crd); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, s := range crd.Spec.Names.ShortNames {
			if builtin, ok := builtinShortNames[s]; ok {
				t.Errorf("%s uses short name %q, which kubectl already maps to %s", crd.Name, s, builtin)
			}
			if other, ok := seen[s]; ok {
				t.Errorf("%s and %s both use short name %q", other, crd.Name, s)
			}
			seen[s] = crd.Name
		}
	}
}
