package controller

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// secretUseCovered lists, per CRD, every spec field that names a Secret or a ServiceAccount, and
// the text the chart's policy for that kind must contain to check it. A new field of that kind
// fails TestSecretUsePolicyCoversEveryReference until the policy and this table both name it.
var secretUseCovered = map[string]map[string]string{
	"mcpservers": {
		"spec.env[].valueFrom.secretKeyRef":            "object.spec.env",
		"spec.registry.authSecretRef":                  "object.spec.registry.authSecretRef",
		"spec.secretRef":                               "object.spec.secretRef",
		"spec.secretVolumes":                           "object.spec.secretVolumes",
		"spec.serviceAccountName":                      "object.spec.serviceAccountName",
		"spec.sidecars[].env[].valueFrom.secretKeyRef": "c.env",
		"spec.sidecars[].envFrom[].secretRef":          "c.envFrom",
	},
	grantsAgents: {
		"spec.deployment.env[].valueFrom.secretKeyRef": "object.spec.deployment.env",
		"spec.deployment.serviceAccountName":           "object.spec.deployment.serviceAccountName",
		"spec.harness.env[].valueFrom.secretKeyRef":    "object.spec.harness.env",
		"spec.memory.secretRef":                        "object.spec.memory.secretRef",
	},
}

// secretFieldPaths walks an OpenAPI schema and returns the path of every property whose name
// mentions a secret or a service account.
func secretFieldPaths(schema map[string]any, prefix string) []string {
	var out []string
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		child, _ := raw.(map[string]any)
		path := prefix + "." + name
		lower := strings.ToLower(name)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "serviceaccount") {
			out = append(out, path)
			continue
		}
		out = append(out, secretFieldPaths(child, path)...)
		if items, ok := child["items"].(map[string]any); ok {
			out = append(out, secretFieldPaths(items, path+"[]")...)
		}
	}
	sort.Strings(out)
	return out
}

func loadSpecSchema(t *testing.T, crdFile string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(crdFile))
	if err != nil {
		t.Fatal(err)
	}
	var crd map[string]any
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	versions := crd["spec"].(map[string]any)["versions"].([]any)
	root := versions[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
	return root["properties"].(map[string]any)["spec"].(map[string]any)
}

func TestSecretUsePolicyCoversEveryReference(t *testing.T) {
	policies, err := os.ReadFile(filepath.Clean(grantsPoliciesRel))
	if err != nil {
		t.Fatal(err)
	}
	docs := strings.Split(string(policies), "\n---\n")
	for crd, covered := range secretUseCovered {
		var policy string
		for _, d := range docs {
			if strings.Contains(d, "resources: [\""+crd+"\"]") {
				policy = d
			}
		}
		if policy == "" {
			t.Fatalf("no policy for %s in %s", crd, grantsPoliciesRel)
		}
		schema := loadSpecSchema(t, "../../config/crd/bases/kubemoot.ai_"+crd+".yaml")
		found := secretFieldPaths(schema, "spec")
		for _, path := range found {
			snippet, ok := covered[path]
			if !ok {
				t.Errorf("%s: field %s references a Secret or ServiceAccount but the use-grant policy does not check it; extend files/secret-use-grants-policies.yaml and secretUseCovered", crd, path)
				continue
			}
			if !strings.Contains(policy, snippet) {
				t.Errorf("%s: the policy no longer mentions %q for %s", crd, snippet, path)
			}
		}
		if len(found) != len(covered) {
			t.Errorf("%s: secretUseCovered lists %d fields, the CRD has %d: %v", crd, len(covered), len(found), found)
		}
	}
}

func TestSecretFieldPaths_FindsNestedAndListFields(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{
		"unrelated": map[string]any{},
		"list": map[string]any{"items": map[string]any{"properties": map[string]any{
			"tokenSecretRef": map[string]any{},
		}}},
		"serviceAccountName": map[string]any{},
	}}
	got := secretFieldPaths(schema, "spec")
	want := []string{"spec.list[].tokenSecretRef", "spec.serviceAccountName"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}
