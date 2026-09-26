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

package controller

import (
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func TestBuildGatewayAuthEnv_Disabled(t *testing.T) {
	gateway := &kubemootv1alpha1.MCPGateway{}
	env := buildGatewayAuthEnv(gateway)
	if len(env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(env))
	}
	if env[0].Name != "AUTH_REQUIRED" || env[0].Value != "false" {
		t.Errorf("expected AUTH_REQUIRED=false, got %s=%s", env[0].Name, env[0].Value)
	}
}

func TestBuildGatewayAuthEnv_NilAuth(t *testing.T) {
	gateway := &kubemootv1alpha1.MCPGateway{
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Auth: nil,
		},
	}
	env := buildGatewayAuthEnv(gateway)
	if len(env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(env))
	}
	if env[0].Value != "false" {
		t.Errorf("expected false, got %s", env[0].Value)
	}
}

func TestBuildGatewayAuthEnv_EnabledNotTrue(t *testing.T) {
	gateway := &kubemootv1alpha1.MCPGateway{
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Auth: &kubemootv1alpha1.MCPGatewayAuth{Enabled: false},
		},
	}
	env := buildGatewayAuthEnv(gateway)
	if len(env) != 1 || env[0].Value != "false" {
		t.Errorf("expected AUTH_REQUIRED=false for disabled auth")
	}
}

func TestBuildGatewayAuthEnv_JWT(t *testing.T) {
	gateway := &kubemootv1alpha1.MCPGateway{
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Auth: &kubemootv1alpha1.MCPGatewayAuth{
				Enabled:      true,
				Type:         "jwt",
				JWTSecretRef: "my-jwt-secret",
			},
		},
	}
	env := buildGatewayAuthEnv(gateway)
	if len(env) != 2 {
		t.Fatalf("expected 2 env vars for JWT auth, got %d", len(env))
	}
	if env[0].Name != "AUTH_REQUIRED" || env[0].Value != "true" {
		t.Errorf("expected AUTH_REQUIRED=true, got %s=%s", env[0].Name, env[0].Value)
	}
	if env[1].Name != "JWT_SECRET_KEY" {
		t.Errorf("expected JWT_SECRET_KEY, got %s", env[1].Name)
	}
	if env[1].ValueFrom == nil || env[1].ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected SecretKeyRef for JWT_SECRET_KEY")
	}
	if env[1].ValueFrom.SecretKeyRef.Name != "my-jwt-secret" {
		t.Errorf("expected secret name my-jwt-secret, got %s", env[1].ValueFrom.SecretKeyRef.Name)
	}
}

func TestBuildGatewayAuthEnv_BasicAuth(t *testing.T) {
	gateway := &kubemootv1alpha1.MCPGateway{
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Auth: &kubemootv1alpha1.MCPGatewayAuth{
				Enabled: true,
				Type:    "basic",
				BasicAuth: &kubemootv1alpha1.BasicAuthConfig{
					SecretRef: "my-basic-secret",
				},
			},
		},
	}
	env := buildGatewayAuthEnv(gateway)
	if len(env) != 3 {
		t.Fatalf("expected 3 env vars for basic auth, got %d", len(env))
	}
	if env[0].Value != "true" {
		t.Errorf("expected AUTH_REQUIRED=true")
	}
	if env[1].Name != "BASIC_AUTH_USER" {
		t.Errorf("expected BASIC_AUTH_USER, got %s", env[1].Name)
	}
	if env[2].Name != "BASIC_AUTH_PASSWORD" {
		t.Errorf("expected BASIC_AUTH_PASSWORD, got %s", env[2].Name)
	}
}

func TestBuildGatewayAuthEnv_EnabledNoType(t *testing.T) {
	gateway := &kubemootv1alpha1.MCPGateway{
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Auth: &kubemootv1alpha1.MCPGatewayAuth{
				Enabled: true,
				Type:    "none",
			},
		},
	}
	env := buildGatewayAuthEnv(gateway)
	// Auth enabled but type=none: only AUTH_REQUIRED=true
	if len(env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(env))
	}
	if env[0].Value != "true" {
		t.Errorf("expected true, got %s", env[0].Value)
	}
}

func TestBuildDynamicMCPServerSpec_OCI(t *testing.T) {
	discovered := kubemootv1alpha1.DiscoveredServer{
		RegistryType:      "oci",
		PackageIdentifier: "ghcr.io/example/mcp-server:v1",
	}
	image, cmd, args, transport, err := buildDynamicMCPServerSpec(discovered, "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if image != "ghcr.io/example/mcp-server:v1" {
		t.Errorf("expected OCI image, got %s", image)
	}
	if len(cmd) != 0 || len(args) != 0 {
		t.Errorf("expected no command/args for OCI, got cmd=%v args=%v", cmd, args)
	}
	if transport != "http" {
		t.Errorf("expected http transport preserved, got %s", string(transport))
	}
}

func TestBuildDynamicMCPServerSpec_NPM(t *testing.T) {
	discovered := kubemootv1alpha1.DiscoveredServer{
		RegistryType:      "npm",
		PackageIdentifier: "@modelcontextprotocol/server-fetch",
	}
	image, cmd, args, transport, err := buildDynamicMCPServerSpec(discovered, "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if image != "node:20-alpine" {
		t.Errorf("expected node:20-alpine, got %s", image)
	}
	if len(cmd) != 1 || cmd[0] != "npx" {
		t.Errorf("expected npx command, got %v", cmd)
	}
	if len(args) != 2 || args[0] != "-y" {
		t.Errorf("expected [-y, package], got %v", args)
	}
	if transport != kubemootv1alpha1.TransportStdio {
		t.Errorf("expected stdio transport for npm, got %s", string(transport))
	}
}

func TestBuildDynamicMCPServerSpec_PyPI(t *testing.T) {
	discovered := kubemootv1alpha1.DiscoveredServer{
		RegistryType:      "pypi",
		PackageIdentifier: "mcp-server-fetch",
	}
	image, cmd, _, transport, err := buildDynamicMCPServerSpec(discovered, "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if image != "python:3.11-alpine" {
		t.Errorf("expected python image, got %s", image)
	}
	if len(cmd) != 2 || cmd[0] != "sh" {
		t.Errorf("expected sh -c command, got %v", cmd)
	}
	if transport != kubemootv1alpha1.TransportStdio {
		t.Errorf("expected stdio transport for pypi, got %s", string(transport))
	}
}

func TestBuildDynamicMCPServerSpec_UnknownRegistry(t *testing.T) {
	discovered := kubemootv1alpha1.DiscoveredServer{
		RegistryType: "unknown",
	}
	_, _, _, _, err := buildDynamicMCPServerSpec(discovered, "http")
	if err == nil {
		t.Error("expected error for unknown registry type")
	}
}

func TestBuildDynamicMCPServerSpec_OCI_NoPackage(t *testing.T) {
	discovered := kubemootv1alpha1.DiscoveredServer{
		RegistryType: "docker",
	}
	_, _, _, _, err := buildDynamicMCPServerSpec(discovered, "http")
	if err == nil {
		t.Error("expected error for empty package identifier")
	}
}

func TestParseInt_Valid(t *testing.T) {
	v, err := parseInt("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
}

func TestParseInt_Invalid(t *testing.T) {
	_, err := parseInt("abc")
	if err == nil {
		t.Error("expected error for non-numeric input")
	}
}

func TestParseFloat_Valid(t *testing.T) {
	v, err := parseFloat("3.14")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v < 3.13 || v > 3.15 {
		t.Errorf("expected ~3.14, got %f", v)
	}
}

func TestParseFloat_Invalid(t *testing.T) {
	_, err := parseFloat("not-a-number")
	if err == nil {
		t.Error("expected error for non-numeric input")
	}
}
