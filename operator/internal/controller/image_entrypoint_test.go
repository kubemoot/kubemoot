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
	"path"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The Kubemoot Go images are buildpacks images: the binary lives under /workspace and the
// image entrypoint (/cnb/process/web) starts it. The pods the operator builds for them
// must therefore leave `command` unset and pass only args, and must not depend on the
// image's user where the component needs a fixed one.

func stdioMCPServer(proxy *kubemootv1alpha1.ProxyInjectionConfig) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: testNamespaceA},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:          "example.org/server:1",
			Transport:      kubemootv1alpha1.TransportStdio,
			Command:        []string{"/workspace/server"},
			Args:           []string{"--stdio"},
			ProxyInjection: proxy,
		},
	}
}

func TestBridgeSidecarStartsThroughTheImageEntrypoint(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	mcp := stdioMCPServer(nil)
	sidecars, _, main := r.applyBridgeSidecar(mcp, buildMCPServerContainer(mcp, 3000), nil, nil)
	if len(sidecars) != 1 {
		t.Fatalf("want one bridge sidecar, got %d", len(sidecars))
	}
	bridge := sidecars[0]
	if len(bridge.Command) != 0 {
		t.Errorf("bridge sidecar sets command %v; the image entrypoint must start the bridge", bridge.Command)
	}
	if len(bridge.Args) == 0 || bridge.Args[0] != "--port" {
		t.Errorf("bridge sidecar args = %v, want the bridge flags", bridge.Args)
	}
	// The bridge copies its own binary into the pipe dir under its file name; the main
	// container runs that copy, so the path is independent of where the image keeps it.
	want := path.Join(bridgePipeDir, "kubemoot-mcp-bridge")
	if len(main.Command) != 1 || main.Command[0] != want {
		t.Errorf("main container command = %v, want [%s]", main.Command, want)
	}
	wantArgs := []string{"exec", "--pipe-dir", bridgePipeDir, "--", "/workspace/server", "--stdio"}
	if len(main.Args) != len(wantArgs) {
		t.Fatalf("main container args = %v, want %v", main.Args, wantArgs)
	}
	for i := range wantArgs {
		if main.Args[i] != wantArgs[i] {
			t.Errorf("main container args = %v, want %v", main.Args, wantArgs)
			break
		}
	}
}

func TestBridgeSidecarRunsAsTheMainContainerUser(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}

	// Non-strict: no user is set, so the bridge runs as its image's (non-root) user.
	mcp := stdioMCPServer(nil)
	c := buildMCPServerContainer(mcp, 3000)
	applyMCPServerSecurityContext(&c, false)
	sidecars, _, _ := r.applyBridgeSidecar(mcp, c, nil, nil)
	if sc := sidecars[0].SecurityContext; sc == nil || sc.RunAsUser != nil {
		t.Errorf("non-strict bridge must take the image user, got %+v", sc)
	}

	// Strict: the pinned uid applies to the bridge too.
	c = buildMCPServerContainer(mcp, 3000)
	applyMCPServerSecurityContext(&c, true)
	sidecars, _, _ = r.applyBridgeSidecar(mcp, c, nil, nil)
	if sc := sidecars[0].SecurityContext; sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 1000 {
		t.Errorf("strict bridge must run as 1000, got %+v", sc)
	}
}

func TestNoBridgeSidecarWithoutStdioOrInjection(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	disabled := false
	cases := map[string]*kubemootv1alpha1.MCPServer{
		"http transport":     {Spec: kubemootv1alpha1.MCPServerSpec{Transport: kubemootv1alpha1.TransportHTTP, Command: []string{"/x"}}},
		"injection disabled": stdioMCPServer(&kubemootv1alpha1.ProxyInjectionConfig{Enabled: &disabled}),
	}
	for name, mcp := range cases {
		c := corev1.Container{Name: "server", Command: mcp.Spec.Command}
		sidecars, _, main := r.applyBridgeSidecar(mcp, c, nil, nil)
		if len(sidecars) != 0 {
			t.Errorf("%s: want no bridge sidecar, got %d", name, len(sidecars))
		}
		if len(main.Command) != len(mcp.Spec.Command) || main.Command[0] != mcp.Spec.Command[0] {
			t.Errorf("%s: main command changed to %v", name, main.Command)
		}
	}
}

func TestDiscussionGatewayStartsThroughTheImageEntrypoint(t *testing.T) {
	r := &CrewReconciler{ConfigCache: NewConfigCache()}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA}}
	containers := r.buildDeployment(crew).Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("want one container, got %d", len(containers))
	}
	c := containers[0]
	if len(c.Command) != 0 || len(c.Args) != 0 {
		t.Errorf("discussion-gateway sets command %v args %v; the image entrypoint must start it", c.Command, c.Args)
	}
	if c.SecurityContext == nil || c.SecurityContext.RunAsUser == nil || c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Errorf("discussion-gateway must pin a non-root uid, got %+v", c.SecurityContext)
	}
}

func TestFitnessRunnerStartsThroughTheImageEntrypoint(t *testing.T) {
	r := &CrewFitnessReconciler{ConfigCache: NewConfigCache()}
	cf := &kubemootv1alpha1.CrewFitness{ObjectMeta: metav1.ObjectMeta{Name: "fit", Namespace: testNamespaceA}}
	containers := r.buildJob(cf, "fit-job", "http://gw", "key", "fit-tests").Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("want one container, got %d", len(containers))
	}
	c := containers[0]
	if len(c.Command) != 0 || len(c.Args) != 0 {
		t.Errorf("fitness-runner sets command %v args %v; the image entrypoint must start it", c.Command, c.Args)
	}
	if c.SecurityContext == nil || c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 1000 {
		t.Errorf("fitness-runner must run as 1000, got %+v", c.SecurityContext)
	}
}
