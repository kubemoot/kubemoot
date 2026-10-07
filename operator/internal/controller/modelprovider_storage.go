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
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// defaultOllamaModelsDir is where Ollama keeps models when OLLAMA_MODELS is unset.
const defaultOllamaModelsDir = "/root/.ollama/models"

// findOllamaPod resolves the provider endpoint to the pod behind it, or nil when
// the endpoint is not a cluster Service or has no ready pod.
func (r *ModelProviderReconciler) findOllamaPod(ctx context.Context, provider *aiv1alpha1.ModelProvider) *corev1.Pod {
	log := logf.FromContext(ctx)
	svcName, svcNamespace := parseServiceFromEndpoint(provider.Spec.Endpoint, provider.Namespace)
	if svcName == "" {
		log.V(1).Info("Could not parse service from endpoint", "endpoint", provider.Spec.Endpoint)
		return nil
	}
	podName, podNamespace := r.findBackingPod(ctx, svcName, svcNamespace)
	if podName == "" {
		return nil
	}
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: podNamespace}, pod); err != nil {
		log.V(1).Info("Failed to get Ollama pod", "pod", podName, "error", err)
		return nil
	}
	return pod
}

// discoverStorage records the disk behind the provider's model directory in
// status.storage. The size comes from the volume the Ollama pod mounts at its
// models directory (a PersistentVolumeClaim's capacity, or an emptyDir's size
// limit); the used part is the sum of the model sizes Ollama lists. A provider
// whose volume cannot be sized gets no storage status. Best effort: a failure
// leaves the previous value untouched.
func (r *ModelProviderReconciler) discoverStorage(ctx context.Context, provider *aiv1alpha1.ModelProvider, httpClient *http.Client) {
	log := logf.FromContext(ctx)
	pod := r.findOllamaPod(ctx, provider)
	if pod == nil {
		return
	}
	volume, total := r.modelVolumeCapacity(ctx, pod)
	if total <= 0 {
		provider.Status.Storage = nil
		return
	}
	models, err := listOllamaTags(ctx, httpClient, provider.Spec.Endpoint)
	if err != nil {
		log.V(1).Info("Could not list models to size provider storage", "error", err)
		return
	}
	var used int64
	for _, m := range models {
		used += m.Size
	}
	now := metav1.Now()
	provider.Status.Storage = &aiv1alpha1.ProviderStorage{
		Volume:     volume,
		TotalBytes: total,
		ModelBytes: used,
		FreeBytes:  max(total-used, 0),
		LastProbed: &now,
	}
}

// modelVolumeCapacity finds the volume mounted at the pod's models directory and
// returns its name and declared size in bytes (0 when it cannot be sized).
func (r *ModelProviderReconciler) modelVolumeCapacity(ctx context.Context, pod *corev1.Pod) (string, int64) {
	name := modelVolumeName(pod)
	if name == "" {
		return "", 0
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name != name {
			continue
		}
		switch {
		case v.PersistentVolumeClaim != nil:
			return "pvc/" + v.PersistentVolumeClaim.ClaimName, r.claimCapacity(ctx, pod.Namespace, v.PersistentVolumeClaim.ClaimName)
		case v.EmptyDir != nil && v.EmptyDir.SizeLimit != nil:
			return "emptyDir/" + v.Name, v.EmptyDir.SizeLimit.Value()
		}
	}
	return "", 0
}

// claimCapacity is the bound size of a PersistentVolumeClaim, falling back to its
// request while it is not bound, and 0 when it cannot be read.
func (r *ModelProviderReconciler) claimCapacity(ctx context.Context, namespace, name string) int64 {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pvc); err != nil {
		logf.FromContext(ctx).V(1).Info("Could not read the PersistentVolumeClaim", "pvc", name, "error", err)
		return 0
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		return q.Value()
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		return q.Value()
	}
	return 0
}

// modelVolumeName returns the name of the pod volume whose mount path is the
// longest prefix of the Ollama models directory, or "" when none contains it.
func modelVolumeName(pod *corev1.Pod) string {
	dir := ollamaModelsDir(pod)
	best, bestLen := "", -1
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			mount := strings.TrimSuffix(m.MountPath, "/")
			if (dir == mount || strings.HasPrefix(dir, mount+"/")) && len(mount) > bestLen {
				best, bestLen = m.Name, len(mount)
			}
		}
	}
	return best
}

// ollamaModelsDir is OLLAMA_MODELS from the pod's containers, or Ollama's default.
func ollamaModelsDir(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "OLLAMA_MODELS" && e.Value != "" {
				return strings.TrimSuffix(e.Value, "/")
			}
		}
	}
	return defaultOllamaModelsDir
}
