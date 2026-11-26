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
	"slices"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The code sandbox image is a buildpacks image that runs the programs agents write. What
// contains them comes from the pod the operator builds, not from the image, so these tests
// hold that pod to the same settings whichever way the image is built: strict mode runs it as
// 1000:1000 (the workspace-access materializer's user), non-root, with every capability
// dropped, no privilege escalation and the RuntimeDefault seccomp profile.

const codeSandboxLauncher = "/cnb/process/web"

func codeSandboxMCPServer(transport kubemootv1alpha1.MCPTransport, command []string) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "code-sandbox", Namespace: testNamespaceA},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:        "ghcr.io/kubemoot/code-sandbox:1.0.0",
			Transport:    transport,
			Command:      command,
			SecurityMode: kubemootv1alpha1.SecurityModeStrict,
		},
	}
}

func TestCodeSandboxPodIsContainedAndStartsThroughTheImageEntrypoint(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	spec := r.buildDeployment(codeSandboxMCPServer(kubemootv1alpha1.TransportHTTP, nil)).Spec.Template.Spec
	if len(spec.Containers) != 1 {
		t.Fatalf("want the server container alone, got %d containers", len(spec.Containers))
	}
	c := spec.Containers[0]
	if len(c.Command) != 0 || len(c.Args) != 0 {
		t.Errorf("code-sandbox sets command %v args %v; the image launcher must start it", c.Command, c.Args)
	}
	assertSandboxContainerContext(t, c.SecurityContext)
	assertSandboxPodContext(t, spec)
}

func assertSandboxContainerContext(t *testing.T, sc *corev1.SecurityContext) {
	t.Helper()
	if sc == nil || !int64Is(sc.RunAsUser, 1000) || !int64Is(sc.RunAsGroup, 1000) {
		t.Fatalf("code-sandbox must run as 1000:1000, got %+v", sc)
	}
	if !boolIs(sc.RunAsNonRoot, true) {
		t.Error("code-sandbox must run as non-root")
	}
	if !boolIs(sc.AllowPrivilegeEscalation, false) {
		t.Error("code-sandbox must not allow privilege escalation")
	}
	if boolIs(sc.Privileged, true) {
		t.Error("code-sandbox must not be privileged")
	}
	if !keepsModeHardening(sc) {
		t.Errorf("code-sandbox must drop every capability and keep the RuntimeDefault seccomp profile, got %+v", sc)
	}
}

func assertSandboxPodContext(t *testing.T, spec corev1.PodSpec) {
	t.Helper()
	if spec.SecurityContext == nil || !boolIs(spec.SecurityContext.RunAsNonRoot, true) {
		t.Errorf("the pod must be non-root, got %+v", spec.SecurityContext)
	}
	if spec.HostPID || spec.HostNetwork || spec.HostIPC {
		t.Error("code-sandbox must not share the host namespaces")
	}
}

func int64Is(p *int64, want int64) bool { return p != nil && *p == want }

func boolIs(p *bool, want bool) bool { return p != nil && *p == want }

// A stdio sandbox is started by the bridge, which runs the command it is given: the
// image's launcher.
func TestCodeSandboxStdioIsStartedByTheLauncher(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	mcp := codeSandboxMCPServer(kubemootv1alpha1.TransportStdio, []string{codeSandboxLauncher})
	_, _, main := r.applyBridgeSidecar(mcp, buildMCPServerContainer(mcp, 3000), nil, nil)
	if i := slices.Index(main.Args, "--"); i < 0 || main.Args[i+1] != codeSandboxLauncher {
		t.Errorf("the bridge must run %s, got args %v", codeSandboxLauncher, main.Args)
	}
}

// The default security mode, relaxed, forces no user, so a Paketo image would run as its own
// user, 1002. The code sandbox's pod spec (the crews' charts) pins 1000:1000 through
// spec.securityContext; the pod then runs as before and keeps the mode's hardening.
func TestRelaxedCodeSandboxPodPinsTheUserThroughTheSecurityContext(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	mcp := codeSandboxMCPServer(kubemootv1alpha1.TransportHTTP, nil)
	mcp.Spec.SecurityMode = kubemootv1alpha1.SecurityModeRelaxed

	unpinned := r.buildDeployment(mcp).Spec.Template.Spec.Containers[0].SecurityContext
	if unpinned == nil || unpinned.RunAsUser != nil {
		t.Fatalf("relaxed mode alone must leave the image user, got %+v", unpinned)
	}

	nonRoot, uid := true, int64(1000)
	mcp.Spec.SecurityContext = &corev1.SecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &uid, RunAsGroup: &uid}
	pinned := r.buildDeployment(mcp).Spec.Template.Spec.Containers[0].SecurityContext
	assertSandboxContainerContext(t, pinned)
}
