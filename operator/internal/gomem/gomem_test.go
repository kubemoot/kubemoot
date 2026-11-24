package gomem

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func limits(mem string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)}}
}

func TestEnvIsNinetyPercentOfTheLimitInWholeMiB(t *testing.T) {
	cases := map[string]string{
		"64Mi":    "57MiB",  // 57.6 truncates
		"128Mi":   "115MiB", // 115.2
		"512Mi":   "460MiB", // 460.8
		"1Gi":     "921MiB", // 921.6
		"1500M":   "1287MiB",
		"1.5Gi":   "1382MiB",
		"1048576": "0",
	}
	for limit, want := range cases {
		got, ok := Env(limits(limit))
		if want == "0" {
			if ok {
				t.Fatalf("limit %s: got %v, want none", limit, got)
			}
			continue
		}
		if !ok || got.Name != "GOMEMLIMIT" || got.Value != want {
			t.Fatalf("limit %s: got %v %v, want %s", limit, got, ok, want)
		}
	}
}

func TestEnvIsNoneWithoutAMemoryLimit(t *testing.T) {
	for name, res := range map[string]corev1.ResourceRequirements{
		"empty":         {},
		"request only":  {Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
		"cpu limit":     {Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
		"zero limit":    limits("0"),
		"under one MiB": limits("500Ki"),
	} {
		if v, ok := Env(res); ok {
			t.Fatalf("%s: got %v, want none", name, v)
		}
	}
}

func TestWithAddsOnceAndKeepsTheUsersValue(t *testing.T) {
	got := With([]corev1.EnvVar{{Name: "A", Value: "1"}}, limits("128Mi"))
	if len(got) != 2 || got[1].Value != "115MiB" {
		t.Fatalf("not added: %v", got)
	}
	user := []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "50MiB"}}
	got = With(user, limits("128Mi"))
	if len(got) != 1 || got[0].Value != "50MiB" {
		t.Fatalf("user value overridden: %v", got)
	}
	if got := With(nil, corev1.ResourceRequirements{}); got != nil {
		t.Fatalf("no limit changed env: %v", got)
	}
}
