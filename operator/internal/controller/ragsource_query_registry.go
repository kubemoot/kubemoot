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

// ragsource_query_registry.go writes the registry of a namespace's Go query service: the
// JSON file (query-service-go/README.md) that lists every RAGSource the service answers for.
// The operator writes it into a ConfigMap, the service reads it from the mounted directory and
// swaps to a new one in one step, so a RAGSource added, removed or moved to a new collection
// (an index swap) reaches the running pods without restarting them.
package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// registryKey is the ConfigMap key, and the file the service reads.
	registryKey = "registry.json"
	// registryDir is where the ConfigMap is mounted, as a directory: a subPath mount never
	// receives an update.
	registryDir = "/etc/kubemoot/query"
	// credentialsRoot holds one directory per vector store Secret.
	credentialsRoot = "/etc/kubemoot/credentials"
	// registryVersionLength is the length of the version the service reports: the start of the
	// SHA-256 of the registry file's bytes.
	registryVersionLength = 12
	// maxRegistryTopK is the most results a query may ask for; a larger default is cut to it.
	maxRegistryTopK = 100
)

// queryRegistry is the registry file's content.
type queryRegistry struct {
	Sources []queryRegistrySource `json:"sources"`
}

// queryRegistrySource is one RAGSource the service answers for.
type queryRegistrySource struct {
	Name        string                 `json:"name"`
	Namespace   string                 `json:"namespace"`
	VectorStore queryRegistryStore     `json:"vectorStore"`
	Embedding   queryRegistryEmbedding `json:"embedding"`
	DefaultTopK int                    `json:"defaultTopK,omitempty"`
}

// queryRegistryStore says where a source's vectors are. The file holds no secret: the
// credentials are read from CredentialsDir, a mounted Secret.
type queryRegistryStore struct {
	Type           string `json:"type"`
	Endpoint       string `json:"endpoint"`
	Collection     string `json:"collection"`
	Dimensions     int32  `json:"dimensions"`
	CredentialsDir string `json:"credentialsDir,omitempty"`
}

// queryRegistryEmbedding says how to embed a question for a source.
type queryRegistryEmbedding struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// credentialsDirFor is where a vector store Secret is mounted in the service pod. One Secret
// shared by several sources is mounted once.
func credentialsDirFor(secretName string) string {
	return path.Join(credentialsRoot, secretName)
}

// registryEntry is what the service needs to answer for the RAGSource. The embedding model is
// the one that made the index readers use now (servingIndex), so a question is embedded the way
// that index's vectors were made, and the collection is the serving one, so an index swap is a
// change to this entry. The second result says why the RAGSource cannot be served yet, or is
// empty.
func registryEntry(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) (queryRegistrySource, string) {
	if em == nil {
		return queryRegistrySource{}, "its EmbeddingModel is not ready"
	}
	vs := rs.Spec.VectorStore
	if vs.Type != kubemootv1alpha1.VectorStorePgvector {
		return queryRegistrySource{}, fmt.Sprintf("the Go query service reads pgvector, not %q", vs.Type)
	}
	serving := servingIndex(rs, em)
	if serving.Endpoint == "" || serving.Model == "" {
		return queryRegistrySource{}, "its EmbeddingModel has no endpoint yet"
	}
	if vs.Endpoint == "" || serving.Collection == "" {
		return queryRegistrySource{}, "its vector store has no endpoint or collection"
	}
	entry := queryRegistrySource{
		Name:      rs.Name,
		Namespace: rs.Namespace,
		VectorStore: queryRegistryStore{
			Type: string(vs.Type), Endpoint: vs.Endpoint, Collection: serving.Collection, Dimensions: serving.Dimensions,
		},
		Embedding:   queryRegistryEmbedding{Endpoint: serving.Endpoint, Model: serving.Model},
		DefaultTopK: registryTopK(rs),
	}
	if vs.SecretRef != "" {
		entry.VectorStore.CredentialsDir = credentialsDirFor(vs.SecretRef)
	}
	return entry, ""
}

// registryTopK is the result count a query gets when it names none: the RAGSource's own
// spec.queryService.topK, cut to what a query may ask for. 0 leaves the service's default.
func registryTopK(rs *kubemootv1alpha1.RAGSource) int {
	if rs.Spec.QueryService == nil || rs.Spec.QueryService.TopK <= 0 {
		return 0
	}
	return int(min(rs.Spec.QueryService.TopK, maxRegistryTopK))
}

// marshalRegistry is the registry file's bytes: the sources in name order, so the same set of
// RAGSources always gives the same bytes and the same version.
func marshalRegistry(entries []queryRegistrySource) ([]byte, error) {
	sorted := append([]queryRegistrySource(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	if sorted == nil {
		sorted = []queryRegistrySource{}
	}
	return json.Marshal(queryRegistry{Sources: sorted})
}

// parseRegistry reads a registry file the operator wrote. An unreadable file reads as empty.
func parseRegistry(data string) map[string]queryRegistrySource {
	var reg queryRegistry
	if err := json.Unmarshal([]byte(data), &reg); err != nil {
		return nil
	}
	out := make(map[string]queryRegistrySource, len(reg.Sources))
	for _, s := range reg.Sources {
		out[s.Name] = s
	}
	return out
}

// registryVersion is the version the service reports on /ready for a registry file: the
// start of the SHA-256 of its bytes. A pod that reports it serves exactly this file.
func registryVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:registryVersionLength]
}

// registryCredentials lists the Secrets the entries read credentials from, once each and in
// name order, so the pod template is the same every time.
func registryCredentials(entries []queryRegistrySource, secretOf map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		name := secretOf[e.Name]
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// credentialVolumeName is the volume that mounts one vector store Secret. The Secret's own
// name may be longer than a volume name or hold dots, so the volume is named by a hash of it.
func credentialVolumeName(secretName string) string {
	sum := sha256.Sum256([]byte(secretName))
	return "creds-" + hex.EncodeToString(sum[:])[:groupNameHashLength]
}

// credentialVolumes mounts each Secret as a directory the service reads again on every rescan, so a
// rotated Secret takes effect without a restart. The volume is optional: a Secret that is
// missing leaves the directory empty and does not keep the pod, and every other source in it,
// from starting; the operator leaves a source whose Secret is missing out of the registry.
func credentialVolumes(secrets []string) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes := make([]corev1.Volume, 0, len(secrets))
	mounts := make([]corev1.VolumeMount, 0, len(secrets))
	optional := true
	for _, name := range secrets {
		vol := credentialVolumeName(name)
		volumes = append(volumes, corev1.Volume{
			Name:         vol,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, Optional: &optional}},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: vol, MountPath: credentialsDirFor(name), ReadOnly: true})
	}
	return volumes, mounts
}
