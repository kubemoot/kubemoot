package controller

import (
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGatewayEnvSetsNoOriginsByDefault(t *testing.T) {
	t.Setenv(gatewayAllowedOriginsEnv, "")
	if _, found := envValue(gatewayEnv("nats://n:4222", nil), "ALLOWED_ORIGINS"); found {
		t.Fatal("ALLOWED_ORIGINS must be absent when no origin is configured")
	}
	t.Setenv(gatewayAllowedOriginsEnv, "  ")
	if _, found := envValue(gatewayEnv("nats://n:4222", nil), "ALLOWED_ORIGINS"); found {
		t.Fatal("a blank list must set nothing")
	}
}

func TestGatewayEnvPassesConfiguredOrigins(t *testing.T) {
	t.Setenv(gatewayAllowedOriginsEnv, "https://a.example.com,https://b.example.com")
	env := gatewayEnv("nats://n:4222", nil)
	if got, _ := envValue(env, "ALLOWED_ORIGINS"); got != "https://a.example.com,https://b.example.com" {
		t.Fatalf("ALLOWED_ORIGINS = %q", got)
	}
	if got, _ := envValue(env, "NATS_URL"); got != "nats://n:4222" {
		t.Fatalf("NATS_URL = %q", got)
	}
}

func TestGatewayEnvDropsBadOriginsAndBlanks(t *testing.T) {
	t.Setenv(gatewayAllowedOriginsEnv, "https://a.example.com, ,*,https://b.example.com/x,https://a.example.com")
	if got, _ := envValue(gatewayEnv("nats://n:4222", nil), "ALLOWED_ORIGINS"); got != "https://a.example.com" {
		t.Fatalf("ALLOWED_ORIGINS = %q", got)
	}
	t.Setenv(gatewayAllowedOriginsEnv, "*")
	if _, found := envValue(gatewayEnv("nats://n:4222", nil), "ALLOWED_ORIGINS"); found {
		t.Fatal("only invalid entries must set nothing")
	}
}

func TestBuiltGatewayDeploymentCarriesTheOrigins(t *testing.T) {
	t.Setenv(gatewayAllowedOriginsEnv, "https://app.example.com")
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: selCrew, Namespace: "ns1"}}
	d := (&CrewReconciler{ConfigCache: NewConfigCache()}).buildDeployment(crew)
	if got, _ := envValue(d.Spec.Template.Spec.Containers[0].Env, "ALLOWED_ORIGINS"); got != "https://app.example.com" {
		t.Fatalf("gateway ALLOWED_ORIGINS = %q", got)
	}
}
