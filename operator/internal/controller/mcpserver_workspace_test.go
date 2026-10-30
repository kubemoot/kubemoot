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
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const sandboxLimit = "256Mi"

func TestWithQueryWorkspaceLeavesNoneAlone(t *testing.T) {
	plain := readOpsServer()
	for _, engine := range []kubemootv1alpha1.ArtifactQueryEngine{"", kubemootv1alpha1.ArtifactQueryEngineNone} {
		if got := withQueryWorkspace(plain, engine); got != plain {
			t.Errorf("engine %q must leave the server untouched", engine)
		}
	}
}

func TestWithQueryWorkspaceAddsVolumeAndTmpdir(t *testing.T) {
	plain := readOpsServer()
	for _, engine := range []kubemootv1alpha1.ArtifactQueryEngine{kubemootv1alpha1.ArtifactQueryEngineSQLite, kubemootv1alpha1.ArtifactQueryEngineDuckDB} {
		got := withQueryWorkspace(plain, engine)
		vols := got.Spec.EmptyDirVolumes
		if len(vols) != 1 || vols[0].MountPath != artifactWorkspaceDir || vols[0].SizeLimit != artifactWorkspaceSizeLimit {
			t.Errorf("%s: volumes = %v", engine, vols)
		}
		if v, ok := envValue(got.Spec.Env, tmpDirEnv); !ok || v != artifactWorkspaceDir {
			t.Errorf("%s: TMPDIR = %q (set %v)", engine, v, ok)
		}
	}
	if len(plain.Spec.EmptyDirVolumes) != 0 || len(plain.Spec.Env) != 0 {
		t.Error("the server the user owns must not be changed")
	}
}

func TestWithQueryWorkspaceKeepsWhatTheServerDeclares(t *testing.T) {
	own := readOpsServer()
	own.Spec.EmptyDirVolumes = []kubemootv1alpha1.EmptyDirVolume{{MountPath: artifactWorkspaceDir, SizeLimit: "2Gi"}}
	own.Spec.Env = []corev1.EnvVar{{Name: tmpDirEnv, Value: "/scratch"}}
	got := withQueryWorkspace(own, kubemootv1alpha1.ArtifactQueryEngineSQLite)
	if len(got.Spec.EmptyDirVolumes) != 1 || got.Spec.EmptyDirVolumes[0].SizeLimit != "2Gi" {
		t.Errorf("a volume the server declares at /artifacts must win: %v", got.Spec.EmptyDirVolumes)
	}
	if len(got.Spec.Env) != 1 || got.Spec.Env[0].Value != "/scratch" {
		t.Errorf("a TMPDIR the server sets must win: %v", got.Spec.Env)
	}
}

// The limit must hold the most one query writes: 8 artifacts of 32 MiB plus the
// 512 MiB DuckDB temp-directory cap.
func TestWorkspaceSizeLimitHoldsOneQuery(t *testing.T) {
	q, err := resource.ParseQuantity(artifactWorkspaceSizeLimit)
	if err != nil || q.Value() < 8*(32<<20)+512<<20 {
		t.Errorf("limit %q too small or unparsable: %v", artifactWorkspaceSizeLimit, err)
	}
}

func TestOtherServersGetNoWorkspace(t *testing.T) {
	cfg := configWithQueryEngine(kubemootv1alpha1.ArtifactQueryEngineSQLite)
	r := &MCPServerReconciler{Client: fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(cfg).Build()}
	own := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{Image: specImage}}
	res, err := r.resolveMCPServerImage(context.Background(), own)
	if err != nil {
		t.Fatal(err)
	}
	if got := withQueryWorkspace(own, res.QueryEngine); got != own {
		t.Error("a server with its own image must get no workspace")
	}
}

func TestSandboxEmptyDirWiringUnchanged(t *testing.T) {
	ms := readOpsServer()
	ms.Spec.KubemootImage = ""
	ms.Spec.Image = specImage
	ms.Spec.EmptyDirVolumes = []kubemootv1alpha1.EmptyDirVolume{{MountPath: artifactWorkspaceDir, SizeLimit: sandboxLimit}}
	vols, mounts := buildMCPServerVolumes(withQueryWorkspace(ms, ""))
	if len(vols) != 1 || len(mounts) != 1 || vols[0].EmptyDir.SizeLimit.String() != sandboxLimit {
		t.Errorf("the sandbox emptyDir must come from its own declaration: %v %v", vols, mounts)
	}
}

// workspaceMount returns the main container's /artifacts mount and the pod volume it names.
func workspaceMount(dep *appsv1.Deployment) (*corev1.VolumeMount, *corev1.Volume) {
	spec := &dep.Spec.Template.Spec
	for _, m := range spec.Containers[0].VolumeMounts {
		if m.MountPath != artifactWorkspaceDir {
			continue
		}
		for i := range spec.Volumes {
			if spec.Volumes[i].Name == m.Name {
				return &m, &spec.Volumes[i]
			}
		}
		return &m, nil
	}
	return nil, nil
}

func TestReconcileGivesTheReadOpsServerAWritableWorkspace(t *testing.T) {
	cases := []struct {
		name   string
		engine kubemootv1alpha1.ArtifactQueryEngine
		want   bool
	}{
		{"sqlite", kubemootv1alpha1.ArtifactQueryEngineSQLite, true},
		{"duckdb", kubemootv1alpha1.ArtifactQueryEngineDuckDB, true},
		{coreTestNone, kubemootv1alpha1.ArtifactQueryEngineNone, false},
		{"unset", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, cli, req := newReadOpsReconciler(t, configWithQueryEngine(tc.engine))
			reconcileTwice(t, r, req)
			dep := &appsv1.Deployment{}
			if err := cli.Get(context.Background(), types.NamespacedName{Name: readOpsName, Namespace: testNS1}, dep); err != nil {
				t.Fatalf("get Deployment: %v", err)
			}
			if tc.want {
				assertWorkspace(t, dep)
			} else {
				assertNoWorkspace(t, dep)
			}
		})
	}
}

func assertNoWorkspace(t *testing.T, dep *appsv1.Deployment) {
	t.Helper()
	mount, _ := workspaceMount(dep)
	_, tmpSet := envValue(dep.Spec.Template.Spec.Containers[0].Env, tmpDirEnv)
	if mount != nil || tmpSet {
		t.Fatalf("no workspace expected: mount %v, TMPDIR set %v", mount, tmpSet)
	}
}

func assertWorkspace(t *testing.T, dep *appsv1.Deployment) {
	t.Helper()
	mount, vol := workspaceMount(dep)
	if mount == nil || vol == nil || vol.EmptyDir == nil || vol.EmptyDir.SizeLimit == nil {
		t.Fatalf("want a size-limited emptyDir at /artifacts: mount %v, volume %v", mount, vol)
	}
	want := resource.MustParse(artifactWorkspaceSizeLimit)
	if vol.EmptyDir.SizeLimit.Cmp(want) != 0 {
		t.Errorf("size limit = %s, want %s", vol.EmptyDir.SizeLimit.String(), want.String())
	}
	if v, ok := envValue(dep.Spec.Template.Spec.Containers[0].Env, tmpDirEnv); !ok || v != artifactWorkspaceDir {
		t.Errorf("TMPDIR = %q (set %v)", v, ok)
	}
}
