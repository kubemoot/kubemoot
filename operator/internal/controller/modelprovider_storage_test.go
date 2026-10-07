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
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	storageNS    = "ollama"
	storagePod   = "ollama-0"
	storageClaim = "ollama-data"
	gib          = int64(1) << 30

	storageData    = "data"
	storageMount   = "/root/.ollama"
	storageScratch = "scratch"
)

func ollamaPod(volumes []corev1.Volume, mounts []corev1.VolumeMount, env ...corev1.EnvVar) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: storagePod, Namespace: storageNS},
		Spec: corev1.PodSpec{
			Volumes:    volumes,
			Containers: []corev1.Container{{Name: "ollama", VolumeMounts: mounts, Env: env}},
		},
	}
}

func claimVolume() corev1.Volume {
	return corev1.Volume{Name: storageData, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: storageClaim}}}
}

func claim(capacity string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: storageClaim, Namespace: storageNS}}
	if capacity != "" {
		pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)}
	}
	return pvc
}

func endpointSlice() *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-x", Namespace: storageNS, Labels: map[string]string{"kubernetes.io/service-name": "ollama"}},
		Endpoints:  []discoveryv1.Endpoint{{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: storagePod, Namespace: storageNS}}},
	}
}

// storageProvider is a provider whose endpoint resolves to the Ollama pod through
// a Service in storageNS, with the Ollama API served by srv.
func storageProvider(srv *httptest.Server) *aiv1alpha1.ModelProvider {
	return &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu", Namespace: testKubemoot},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: srv.URL},
	}
}

func tagsServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testPathAPITags {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
}

// discoverStorageFor runs discoverStorage with the pod resolved directly, so the
// test does not depend on the endpoint being a cluster Service name.
func discoverStorageFor(t *testing.T, srv *httptest.Server, objs ...client.Object) *aiv1alpha1.ProviderStorage {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(objs...).Build()
	r := &ModelProviderReconciler{Client: cl}
	provider := storageProvider(srv)
	provider.Spec.Endpoint = "http://ollama." + storageNS + ":11434"
	// Serve the Ollama API from the test server while the endpoint names the Service.
	httpClient := &http.Client{Transport: redirectTo(srv)}
	r.discoverStorage(context.Background(), provider, httpClient)
	return provider.Status.Storage
}

// redirectTo sends every request to srv whatever host it names.
func redirectTo(srv *httptest.Server) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(clone)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoverStorage_PVCCapacityMinusModels(t *testing.T) {
	srv := tagsServer(`{"models":[{"name":"a:1","size":1073741824},{"name":"b:1","size":2147483648}]}`)
	defer srv.Close()
	pod := ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: storageMount}})
	got := discoverStorageFor(t, srv, pod, claim("10Gi"), endpointSlice())
	if got == nil {
		t.Fatal("storage not reported")
	}
	if got.Volume != "pvc/"+storageClaim || got.TotalBytes != 10*gib || got.ModelBytes != 3*gib || got.FreeBytes != 7*gib {
		t.Errorf("storage = %+v, want pvc/%s total 10Gi models 3Gi free 7Gi", got, storageClaim)
	}
	if got.LastProbed == nil {
		t.Error("lastProbed not set")
	}
}

func TestDiscoverStorage_FreeNeverNegative(t *testing.T) {
	srv := tagsServer(`{"models":[{"name":"a:1","size":2147483648}]}`)
	defer srv.Close()
	pod := ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: storageMount}})
	got := discoverStorageFor(t, srv, pod, claim("1Gi"), endpointSlice())
	if got == nil || got.FreeBytes != 0 {
		t.Errorf("storage = %+v, want free clamped to 0", got)
	}
}

func TestDiscoverStorage_EmptyDirWithLimit(t *testing.T) {
	srv := tagsServer(`{"models":[]}`)
	defer srv.Close()
	limit := resource.MustParse("4Gi")
	vol := corev1.Volume{Name: storageScratch, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}
	pod := ollamaPod([]corev1.Volume{vol}, []corev1.VolumeMount{{Name: storageScratch, MountPath: "/models"}},
		corev1.EnvVar{Name: "OLLAMA_MODELS", Value: "/models/"})
	got := discoverStorageFor(t, srv, pod, endpointSlice())
	if got == nil || got.Volume != "emptyDir/"+storageScratch || got.FreeBytes != 4*gib {
		t.Errorf("storage = %+v, want emptyDir/scratch with 4Gi free", got)
	}
}

func TestDiscoverStorage_UnsizedVolumesReportNothing(t *testing.T) {
	srv := tagsServer(`{"models":[]}`)
	defer srv.Close()
	noLimit := corev1.Volume{Name: storageScratch, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	hostPath := corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/mnt"}}}
	cases := map[string][]client.Object{
		"emptyDir without a limit": {ollamaPod([]corev1.Volume{noLimit}, []corev1.VolumeMount{{Name: storageScratch, MountPath: storageMount}}), endpointSlice()},
		"hostPath":                 {ollamaPod([]corev1.Volume{hostPath}, []corev1.VolumeMount{{Name: "host", MountPath: storageMount}}), endpointSlice()},
		"models dir not mounted":   {ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: "/other"}}), claim("10Gi"), endpointSlice()},
		"claim missing":            {ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: storageMount}}), endpointSlice()},
		"claim without a size":     {ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: storageMount}}), claim(""), endpointSlice()},
		"no pod behind endpoint":   {},
	}
	for name, objs := range cases {
		if got := discoverStorageFor(t, srv, objs...); got != nil {
			t.Errorf("%s: storage = %+v, want none", name, got)
		}
	}
}

func TestDiscoverStorage_TagsFailureKeepsPreviousValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	pod := ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: storageMount}})
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(pod, claim("10Gi"), endpointSlice()).Build()
	r := &ModelProviderReconciler{Client: cl}
	provider := storageProvider(srv)
	provider.Spec.Endpoint = "http://ollama." + storageNS + ":11434"
	previous := &aiv1alpha1.ProviderStorage{FreeBytes: 42}
	provider.Status.Storage = previous
	r.discoverStorage(context.Background(), provider, &http.Client{Transport: redirectTo(srv)})
	if provider.Status.Storage != previous {
		t.Errorf("storage = %+v, want the previous value kept when /api/tags fails", provider.Status.Storage)
	}
}

func TestModelVolumeName_LongestMountWins(t *testing.T) {
	pod := ollamaPod(nil, []corev1.VolumeMount{
		{Name: "root", MountPath: "/root"},
		{Name: "models", MountPath: "/root/.ollama/models"},
		{Name: "rootlike", MountPath: "/root/.ol"},
	})
	if got := modelVolumeName(pod); got != "models" {
		t.Errorf("modelVolumeName = %q, want the most specific mount (models)", got)
	}
}

func TestOllamaModelsDir(t *testing.T) {
	if got := ollamaModelsDir(ollamaPod(nil, nil)); got != defaultOllamaModelsDir {
		t.Errorf("default dir = %q", got)
	}
	pod := ollamaPod(nil, nil, corev1.EnvVar{Name: "OLLAMA_MODELS", Value: "/data/models/"})
	if got := ollamaModelsDir(pod); got != "/data/models" {
		t.Errorf("OLLAMA_MODELS dir = %q, want trailing slash trimmed", got)
	}
}
