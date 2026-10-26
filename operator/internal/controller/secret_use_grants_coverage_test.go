package controller

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// useGrantCheck says where the policy must check one spec field: the variable that collects its
// names, and a pattern that must match that variable's expression (the field's accessor, with
// word boundaries so that c.env does not stand in for c.envFrom).
type useGrantCheck struct {
	variable string
	accessor *regexp.Regexp
}

// secretUseCovered lists, per CRD, every spec field that names a Secret or a ServiceAccount.
// A new field of that kind fails TestSecretUsePolicyCoversEveryReference until the chart
// template and this table both name it.
var secretUseCovered = map[string]map[string]useGrantCheck{
	grantsMCPServers: {
		"spec.env[].valueFrom.secretKeyRef":            {"envNames", regexp.MustCompile(`object\.spec\.env\.filter\(.*secretKeyRef\.name`)},
		"spec.registry.authSecretRef":                  {"authNames", regexp.MustCompile(`object\.spec\.registry\.authSecretRef\b`)},
		"spec.secretRef":                               {"secretRefNames", regexp.MustCompile(`object\.spec\.secretRef\b`)},
		"spec.secretVolumes":                           {"volumeNames", regexp.MustCompile(`object\.spec\.secretVolumes\b`)},
		"spec.serviceAccountName":                      {"accountNames", regexp.MustCompile(`object\.spec\.serviceAccountName\b`)},
		"spec.sidecars[].env[].valueFrom.secretKeyRef": {"sidecarNames", regexp.MustCompile(`\bc\.env\.filter\(.*e\.valueFrom\.secretKeyRef\.name`)},
		"spec.sidecars[].envFrom[].secretRef":          {"sidecarNames", regexp.MustCompile(`\bc\.envFrom\.filter\(.*f\.secretRef\.name`)},
	},
	grantsAgents: {
		"spec.deployment.env[].valueFrom.secretKeyRef": {"envNames", regexp.MustCompile(`object\.spec\.deployment\.env\.filter\(.*secretKeyRef\.name`)},
		"spec.deployment.serviceAccountName":           {"accountNames", regexp.MustCompile(`object\.spec\.deployment\.serviceAccountName\b`)},
		"spec.harness.env[].valueFrom.secretKeyRef":    {"harnessEnvNames", regexp.MustCompile(`object\.spec\.harness\.env\.filter\(.*secretKeyRef\.name`)},
		"spec.memory.secretRef":                        {"memoryNames", regexp.MustCompile(`object\.spec\.memory\.secretRef\b`)},
	},
}

var secretUsePolicySuffix = map[string]string{grantsMCPServers: grantsMCPKind, grantsAgents: grantsAgentKind}

// secretFieldPaths walks an OpenAPI schema and returns the path of every property whose name
// mentions a secret or a service account. It follows properties, items, additionalProperties and
// allOf, anyOf and oneOf, and reports an object that preserves unknown fields (where any field
// could hide) as a path ending in "{unknown}".
const schemaProperties = "properties"

func secretFieldPaths(schema map[string]any, prefix string) []string {
	var out []string
	if v, _ := schema["x-kubernetes-preserve-unknown-fields"].(bool); v {
		out = append(out, prefix+"{unknown}")
	}
	props, _ := schema[schemaProperties].(map[string]any)
	for name, raw := range props {
		child, _ := raw.(map[string]any)
		path := prefix + "." + name
		lower := strings.ToLower(name)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "serviceaccount") {
			out = append(out, path)
			continue
		}
		out = append(out, secretFieldPaths(child, path)...)
	}
	for key, suffix := range map[string]string{"items": "[]", "additionalProperties": "{}"} {
		if sub, ok := schema[key].(map[string]any); ok {
			out = append(out, secretFieldPaths(sub, prefix+suffix)...)
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		subs, _ := schema[key].([]any)
		for _, raw := range subs {
			sub, _ := raw.(map[string]any)
			out = append(out, secretFieldPaths(sub, prefix)...)
		}
	}
	sort.Strings(out)
	return out
}

// secretFieldsOfCRD returns the secret-like paths in the spec of every version of a CRD.
func secretFieldsOfCRD(t *testing.T, crdFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(crdFile))
	if err != nil {
		t.Fatal(err)
	}
	var crd struct {
		Spec struct {
			Versions []struct {
				Name   string `json:"name"`
				Schema struct {
					OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, v := range crd.Spec.Versions {
		props, _ := v.Schema.OpenAPIV3Schema[schemaProperties].(map[string]any)
		spec, _ := props["spec"].(map[string]any)
		for _, p := range secretFieldPaths(spec, "spec") {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func TestSecretUsePolicyCoversEveryReference(t *testing.T) {
	objs, err := renderUseGrants()
	if err != nil {
		t.Fatal(err)
	}
	for crd, covered := range secretUseCovered {
		policy := policyNamed(objs, secretUsePolicySuffix[crd])
		if policy == nil {
			t.Fatalf("the chart renders no use-grant policy for %s", crd)
		}
		variables, validations := policyExpressions(t, policy.Object)
		found := secretFieldsOfCRD(t, "../../config/crd/bases/kubemoot.ai_"+crd+".yaml")
		for _, path := range found {
			check, ok := covered[path]
			if !ok {
				t.Errorf("%s: field %s can reference a Secret or ServiceAccount but is not in secretUseCovered; check it in the chart template and list it here", crd, path)
				continue
			}
			expr, defined := variables[check.variable]
			switch {
			case !defined:
				t.Errorf("%s: %s: the policy has no variable %q", crd, path, check.variable)
			case !check.accessor.MatchString(expr):
				t.Errorf("%s: %s: variable %q does not read the field (%s)", crd, path, check.variable, check.accessor)
			case !anyContains(validations, "variables."+check.variable+".all("):
				t.Errorf("%s: %s: no validation checks variable %q", crd, path, check.variable)
			}
		}
		if len(found) != len(covered) {
			t.Errorf("%s: secretUseCovered lists %d fields, the CRD has %d: %v", crd, len(covered), len(found), found)
		}
	}
}

func policyExpressions(t *testing.T, obj map[string]any) (map[string]string, []string) {
	t.Helper()
	spec, _ := obj["spec"].(map[string]any)
	variables := map[string]string{}
	vars, _ := spec["variables"].([]any)
	for _, raw := range vars {
		v, _ := raw.(map[string]any)
		variables[v["name"].(string)], _ = v["expression"].(string)
	}
	vals, _ := spec["validations"].([]any)
	validations := make([]string, 0, len(vals))
	for _, raw := range vals {
		v, _ := raw.(map[string]any)
		expr, _ := v["expression"].(string)
		validations = append(validations, expr)
	}
	return variables, validations
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestSecretFieldPaths_FindsEverySchemaShape(t *testing.T) {
	schema := map[string]any{schemaProperties: map[string]any{
		"ordinary": map[string]any{},
		"list": map[string]any{"items": map[string]any{schemaProperties: map[string]any{
			"tokenSecretRef": map[string]any{},
		}}},
		"byName": map[string]any{"additionalProperties": map[string]any{schemaProperties: map[string]any{
			"secretName": map[string]any{},
		}}},
		"either": map[string]any{"allOf": []any{map[string]any{schemaProperties: map[string]any{
			"serviceAccount": map[string]any{},
		}}}},
		"free":               map[string]any{"x-kubernetes-preserve-unknown-fields": true},
		"serviceAccountName": map[string]any{},
	}}
	got := strings.Join(secretFieldPaths(schema, "spec"), ",")
	want := "spec.byName{}.secretName,spec.either.serviceAccount,spec.free{unknown},spec.list[].tokenSecretRef,spec.serviceAccountName"
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
