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
	"fmt"
	"regexp"
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dns1123Label matches a valid Kubernetes resource name / label value segment.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestBoundedName(t *testing.T) {
	// The real crew names that triggered the bug: ADL fits, prose does not.
	adlK8s := "crew-homelab-pilot-homelab-pilot-crew-kubernetes-mcp"                         // 52
	adlLegacy := "crew-homelab-pilot-homelab-pilot-crew-kubernetes-legacy-mcp"               // 59
	proseK8s := "crew-homelab-pilot-prose-homelab-pilot-crew-prose-kubernetes-mcp"           // 64
	proseLegacy := "crew-homelab-pilot-prose-homelab-pilot-crew-prose-kubernetes-legacy-mcp" // 71

	// Names within the limit pass through unchanged (no disruption to existing deployments).
	for _, n := range []string{"", "short", "k8sgpt-mcp", adlK8s, adlLegacy} {
		if got := boundedName(n); got != n {
			t.Errorf("boundedName(%q) = %q; want unchanged", n, got)
		}
	}
	// A name of exactly 63 chars is still unchanged (boundary).
	exact63 := strings.Repeat("a", 63)
	if got := boundedName(exact63); got != exact63 {
		t.Errorf("boundedName(63-char) = %q (len %d); want unchanged", got, len(got))
	}

	// Over-limit names are truncated to exactly 63, stay valid DNS-1123 labels, and differ per input.
	seen := map[string]string{}
	for _, n := range []string{proseK8s, proseLegacy} {
		got := assertBoundedLongName(t, n)
		seen[got] = n
	}
	if len(seen) != 2 {
		t.Errorf("distinct long names collided after bounding: %v", seen)
	}

	// The tool-indexing RAGSource name is boundedName(mcpServer.Name+"-tools").
	// For adlLegacy (59, a passthrough as a Service name) the "+-tools" form is 65
	// and MUST truncate to a valid <=63 label, distinct from the Service name.
	toolsName := boundedName(adlLegacy + "-tools")
	if len(toolsName) != 63 || !dns1123Label.MatchString(toolsName) {
		t.Errorf("boundedName(%q+\"-tools\") = %q (len %d); want valid 63-char label", adlLegacy, toolsName, len(toolsName))
	}
	if toolsName == boundedName(adlLegacy) {
		t.Errorf("RAGSource name collides with Service name for %q", adlLegacy)
	}
}

// assertBoundedLongName checks that an over-limit name bounds to a deterministic,
// valid 63-character DNS-1123 label, and returns that label.
func assertBoundedLongName(t *testing.T, n string) string {
	t.Helper()
	got := boundedName(n)
	if len(got) != 63 {
		t.Errorf("boundedName(%q) len = %d; want 63", n, len(got))
	}
	if !dns1123Label.MatchString(got) {
		t.Errorf("boundedName(%q) = %q is not a valid DNS-1123 label", n, got)
	}
	// Deterministic: same input, same output.
	if boundedName(n) != got {
		t.Errorf("boundedName(%q) is not deterministic", n)
	}
	return got
}

func TestBuildMCPServerContainer_Defaults(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image: "ghcr.io/example/mcp:v1",
		},
	}
	c := buildMCPServerContainer(mcpServer, 3000)

	if c.Name != componentMCPServer {
		t.Errorf("expected name %s, got %s", componentMCPServer, c.Name)
	}
	if c.Image != "ghcr.io/example/mcp:v1" {
		t.Errorf("expected image ghcr.io/example/mcp:v1, got %s", c.Image)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 3000 {
		t.Errorf("expected port 3000, got %v", c.Ports)
	}
	if c.Resources.Requests.Cpu().String() != "100m" {
		t.Errorf("expected default CPU request 100m, got %s", c.Resources.Requests.Cpu().String())
	}
}

func TestBuildMCPServerContainer_WithCommandAndArgs(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:   "node:20-alpine",
			Command: []string{testNpx},
			Args:    []string{"-y", "some-package"},
		},
	}
	c := buildMCPServerContainer(mcpServer, 8080)

	if len(c.Command) != 1 || c.Command[0] != testNpx {
		t.Errorf("expected command [npx], got %v", c.Command)
	}
	if len(c.Args) != 2 || c.Args[0] != "-y" {
		t.Errorf("expected args [-y some-package], got %v", c.Args)
	}
}

func TestBuildMCPServerContainer_WithSecretRef(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:     "example:v1",
			SecretRef: testSecretName,
		},
	}
	c := buildMCPServerContainer(mcpServer, 3000)

	if len(c.EnvFrom) != 1 {
		t.Fatalf("expected 1 envFrom, got %d", len(c.EnvFrom))
	}
	if c.EnvFrom[0].SecretRef.Name != testSecretName {
		t.Errorf("expected secret ref my-secret, got %s", c.EnvFrom[0].SecretRef.Name)
	}
}

func TestBuildMCPServerContainer_WithCustomResources(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image: "example:v1",
			Resources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("200m"),
				},
			},
		},
	}
	c := buildMCPServerContainer(mcpServer, 3000)

	if c.Resources.Requests.Cpu().String() != "200m" {
		t.Errorf("expected custom CPU 200m, got %s", c.Resources.Requests.Cpu().String())
	}
}

func TestApplyMCPServerProbes_HTTPHealthPath(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport:  testHTTP,
			HealthPath: testPathHealth,
		},
	}
	c := &corev1.Container{}
	applyMCPServerProbes(c, mcpServer, 8080)

	if c.LivenessProbe == nil {
		t.Fatal("expected liveness probe")
	}
	if c.LivenessProbe.HTTPGet == nil {
		t.Fatal("expected HTTP liveness probe")
	}
	if c.LivenessProbe.HTTPGet.Path != testPathHealth {
		t.Errorf("expected /health, got %s", c.LivenessProbe.HTTPGet.Path)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Fatal("expected HTTP readiness probe")
	}
	// readiness should default to health path when readiness not specified
	if c.ReadinessProbe.HTTPGet.Path != testPathHealth {
		t.Errorf("expected /health for readiness, got %s", c.ReadinessProbe.HTTPGet.Path)
	}
}

func TestApplyMCPServerProbes_CustomReadinessPath(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport:     testSSE,
			HealthPath:    testPathHealth,
			ReadinessPath: "/ready",
		},
	}
	c := &corev1.Container{}
	applyMCPServerProbes(c, mcpServer, 8080)

	if c.ReadinessProbe.HTTPGet.Path != "/ready" {
		t.Errorf("expected /ready, got %s", c.ReadinessProbe.HTTPGet.Path)
	}
}

func TestApplyMCPServerProbes_NoHealthPath_TCP(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: testHTTP,
		},
	}
	c := &corev1.Container{}
	applyMCPServerProbes(c, mcpServer, 3000)

	if c.LivenessProbe == nil || c.LivenessProbe.TCPSocket == nil {
		t.Fatal("expected TCP liveness probe when no health path")
	}
	if c.LivenessProbe.TCPSocket.Port.IntValue() != 3000 {
		t.Errorf("expected port 3000, got %d", c.LivenessProbe.TCPSocket.Port.IntValue())
	}
}

func TestApplyMCPServerProbes_Stdio_NoProbes(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport:  kubemootv1alpha1.TransportStdio,
			HealthPath: testPathHealth,
		},
	}
	c := &corev1.Container{}
	applyMCPServerProbes(c, mcpServer, 8080)

	if c.LivenessProbe != nil || c.ReadinessProbe != nil {
		t.Error("expected no probes for stdio transport")
	}
}

func TestApplyMCPServerSecurityContext_Strict(t *testing.T) {
	c := &corev1.Container{}
	profile := applyMCPServerSecurityContext(c, true)

	if profile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("expected RuntimeDefault seccomp, got %s", profile.Type)
	}
	if c.SecurityContext == nil {
		t.Fatal("expected security context")
	}
	if c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Error("expected RunAsNonRoot=true for strict")
	}
	if c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 1000 {
		t.Error("expected RunAsUser=1000 for strict")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("expected AllowPrivilegeEscalation=false")
	}
}

func TestApplyMCPServerSecurityContext_Relaxed(t *testing.T) {
	c := &corev1.Container{}
	applyMCPServerSecurityContext(c, false)

	if c.SecurityContext == nil {
		t.Fatal("expected security context")
	}
	if c.SecurityContext.RunAsNonRoot != nil {
		t.Error("expected no RunAsNonRoot for relaxed mode")
	}
	if c.SecurityContext.RunAsUser != nil {
		t.Error("expected no RunAsUser for relaxed mode")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("expected AllowPrivilegeEscalation=false even in relaxed")
	}
}

func TestBuildMCPServerVolumes_Empty(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{}
	vols, mounts := buildMCPServerVolumes(mcpServer)
	if len(vols) != 0 || len(mounts) != 0 {
		t.Errorf("expected empty volumes, got %d vols, %d mounts", len(vols), len(mounts))
	}
}

func TestBuildMCPServerVolumes_SecretVolumes(t *testing.T) {
	readOnly := false
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			SecretVolumes: []kubemootv1alpha1.SecretVolume{
				{Name: testSecretName, MountPath: "/etc/config", SubPath: "config.yaml", ReadOnly: &readOnly},
				{Name: "other-secret", MountPath: "/etc/other"},
			},
		},
	}
	vols, mounts := buildMCPServerVolumes(mcpServer)
	if len(vols) != 2 || len(mounts) != 2 {
		t.Fatalf("expected 2 volumes, got %d vols, %d mounts", len(vols), len(mounts))
	}
	if vols[0].Secret.SecretName != testSecretName {
		t.Errorf("expected my-secret, got %s", vols[0].Secret.SecretName)
	}
	if mounts[0].SubPath != "config.yaml" {
		t.Errorf("expected subPath config.yaml, got %s", mounts[0].SubPath)
	}
	if mounts[0].ReadOnly {
		t.Error("expected readOnly=false when explicitly set")
	}
	// Second mount should default to readOnly=true
	if !mounts[1].ReadOnly {
		t.Error("expected readOnly=true by default")
	}
}

func TestBuildMCPServerVolumes_EmptyDirVolumes(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{
				{MountPath: "/data", SizeLimit: "100Mi"},
				{MountPath: "/tmp"},
			},
		},
	}
	vols, mounts := buildMCPServerVolumes(mcpServer)
	if len(vols) != 2 || len(mounts) != 2 {
		t.Fatalf("expected 2 volumes, got %d vols, %d mounts", len(vols), len(mounts))
	}
	if vols[0].EmptyDir == nil {
		t.Fatal("expected emptyDir volume")
	}
	if vols[0].EmptyDir.SizeLimit == nil {
		t.Fatal("expected sizeLimit on first volume")
	}
	if vols[1].EmptyDir.SizeLimit != nil {
		t.Error("expected no sizeLimit on second volume")
	}
}

// TestBuildMCPServerEndpoint_LongName pins the gateway-connectivity invariant:
// for an over-limit MCPServer name the endpoint host MUST be the bounded name
// (matching the bounded Service name), never the raw 71-char name - otherwise the
// gateway, which connects via Status.Endpoint, would target a non-resolving host.
func TestBuildMCPServerEndpoint_LongName(t *testing.T) {
	longName := "crew-homelab-pilot-prose-homelab-pilot-crew-prose-kubernetes-legacy-mcp" // 71
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      longName,
			Namespace: "crew-homelab-pilot-prose",
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: testHTTP,
			Port:      9090,
		},
	}
	endpoint := buildMCPServerEndpoint(mcpServer)
	expected := fmt.Sprintf(svcEndpointFmt, boundedName(longName), "crew-homelab-pilot-prose", int32(9090))
	if endpoint != expected {
		t.Errorf("endpoint = %s; want bounded host %s", endpoint, expected)
	}
	if strings.Contains(endpoint, longName) {
		t.Errorf("endpoint %s contains the raw 71-char name; host must be bounded", endpoint)
	}
}

func TestBuildMCPServerEndpoint_HTTPTransport(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-server",
			Namespace: testKubemoot,
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: testHTTP,
			Port:      9090,
		},
	}
	endpoint := buildMCPServerEndpoint(mcpServer)
	expected := fmt.Sprintf(svcEndpointFmt, "my-server", testKubemoot, int32(9090))
	if endpoint != expected {
		t.Errorf("expected %s, got %s", expected, endpoint)
	}
}

func TestBuildMCPServerEndpoint_DefaultPort(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-server",
			Namespace: testDefaultNS,
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: testSSE,
		},
	}
	endpoint := buildMCPServerEndpoint(mcpServer)
	expected := fmt.Sprintf(svcEndpointFmt, "my-server", testDefaultNS, int32(3000))
	if endpoint != expected {
		t.Errorf("expected %s, got %s", expected, endpoint)
	}
}

func TestBuildMCPServerEndpoint_Stdio_ProxyEnabled(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stdio-server",
			Namespace: testKubemoot,
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: kubemootv1alpha1.TransportStdio,
		},
	}
	endpoint := buildMCPServerEndpoint(mcpServer)
	// Default proxy port is 8080
	expected := fmt.Sprintf(svcEndpointFmt, "stdio-server", testKubemoot, int32(8080))
	if endpoint != expected {
		t.Errorf("expected %s, got %s", expected, endpoint)
	}
}

func TestBuildMCPServerEndpoint_Stdio_ProxyDisabled(t *testing.T) {
	proxyEnabled := false
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stdio-server",
			Namespace: testKubemoot,
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: kubemootv1alpha1.TransportStdio,
			ProxyInjection: &kubemootv1alpha1.ProxyInjectionConfig{
				Enabled: &proxyEnabled,
			},
		},
	}
	endpoint := buildMCPServerEndpoint(mcpServer)
	if endpoint != "" {
		t.Errorf("expected empty endpoint when proxy disabled, got %s", endpoint)
	}
}

func TestBuildMCPServerEndpoint_Stdio_CustomProxyPort(t *testing.T) {
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-port",
			Namespace: "ns",
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Transport: kubemootv1alpha1.TransportStdio,
			ProxyInjection: &kubemootv1alpha1.ProxyInjectionConfig{
				Port: 9999,
			},
		},
	}
	endpoint := buildMCPServerEndpoint(mcpServer)
	expected := fmt.Sprintf(svcEndpointFmt, "custom-port", "ns", int32(9999))
	if endpoint != expected {
		t.Errorf("expected %s, got %s", expected, endpoint)
	}
}

func TestInt64Ptr(t *testing.T) {
	v := int64Ptr(42)
	if *v != 42 {
		t.Errorf("expected 42, got %d", *v)
	}
}

func TestBoolPtr(t *testing.T) {
	v := boolPtr(true)
	if !*v {
		t.Error("expected true")
	}
	v = boolPtr(false)
	if *v {
		t.Error("expected false")
	}
}

func TestBuildUserSidecars(t *testing.T) {
	// No sidecars -> nil.
	if got := buildUserSidecars(&kubemootv1alpha1.MCPServer{}); got != nil {
		t.Fatalf("expected nil for no sidecars, got %v", got)
	}

	// Declared sidecars are rendered as native sidecars (RestartPolicy=Always),
	// even when the caller did not set RestartPolicy, with other fields preserved.
	ms := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			Sidecars: []corev1.Container{
				{Name: "artifact-access", Image: "img:1", VolumeMounts: []corev1.VolumeMount{{Name: "artifacts", MountPath: testArtifactsDir}}},
			},
		},
	}
	got := buildUserSidecars(ms)
	if len(got) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(got))
	}
	if got[0].RestartPolicy == nil || *got[0].RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatalf("expected RestartPolicy=Always, got %v", got[0].RestartPolicy)
	}
	if got[0].Name != "artifact-access" || got[0].Image != "img:1" || len(got[0].VolumeMounts) != 1 {
		t.Fatalf("sidecar fields not preserved: %+v", got[0])
	}
	// Source must not be mutated (DeepCopy).
	if ms.Spec.Sidecars[0].RestartPolicy != nil {
		t.Fatalf("source sidecar was mutated")
	}
}

func TestBuildUserSidecars_SharesEmptyDir(t *testing.T) {
	// A sidecar automatically gets the pod's EmptyDirVolumes mounted at the same
	// path the main container sees (EmptyDirVolume has no user-settable name).
	ms := &kubemootv1alpha1.MCPServer{
		Spec: kubemootv1alpha1.MCPServerSpec{
			EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: testArtifactsDir, SizeLimit: "256Mi"}},
			Sidecars:        []corev1.Container{{Name: "materializer", Image: "img"}},
		},
	}
	got := buildUserSidecars(ms)
	if len(got) != 1 || len(got[0].VolumeMounts) != 1 {
		t.Fatalf("expected the emptyDir auto-mounted into the sidecar, got %+v", got)
	}
	vm := got[0].VolumeMounts[0]
	if vm.Name != "empty-vol-0" || vm.MountPath != testArtifactsDir {
		t.Fatalf("expected empty-vol-0 at /artifacts, got %+v", vm)
	}
}
