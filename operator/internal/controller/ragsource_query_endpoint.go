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
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The names of the objects of a namespace's Go query service. The Deployment and the Service
// share the group name; the registry ConfigMap adds a suffix.
const (
	queryGroupSuffix    = "-query"
	registrySuffix      = "-registry"
	maxServiceNameLen   = 63
	groupNameHashLength = 8
)

// labelQueryGroup is on every object of a namespace's Go query service and holds the group's
// name. A per-RAGSource Deployment of the Python engine never carries it, so the label tells
// the two engines' objects apart even when a RAGSource is named like its namespace.
const labelQueryGroup = "kubemoot.ai/query-group"

// queryGroupName is the name of the Deployment and Service that serve every RAGSource of a
// namespace: the namespace and "-query". A Service name is at most 63 characters, so the
// name of a long namespace is cut and given a hash of the whole namespace, which keeps
// two long namespaces apart.
func queryGroupName(namespace string) string {
	name := namespace + queryGroupSuffix
	if len(name) <= maxServiceNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(namespace))
	keep := maxServiceNameLen - len(queryGroupSuffix) - groupNameHashLength - 1
	return fmt.Sprintf("%s-%s%s", namespace[:keep], hex.EncodeToString(sum[:])[:groupNameHashLength], queryGroupSuffix)
}

// queryRegistryName is the name of the ConfigMap that holds a namespace's registry.
func queryRegistryName(namespace string) string {
	return queryGroupName(namespace) + registrySuffix
}

// ragQueryEndpoint is the one place that builds the in-cluster URL a RAGSource is queried at.
// The Python engine runs a Service per RAGSource, <name>-query, and the RAGSource is the whole
// service. The Go engine runs one Service per namespace, always on the default port (port is not
// per RAGSource there), and names the RAGSource in the path, so
// callers that append /query, /info or /mcp need no change. Every fallback that cannot read
// status.queryEndpoint (an agent whose RAGSource is not applied yet, the crew's resume
// search) builds it here.
func ragQueryEndpoint(engine kubemootv1alpha1.QueryServiceEngine, name, namespace string, port int32) string {
	if engine == kubemootv1alpha1.QueryServiceEngineGo {
		return fmt.Sprintf("http://%s.%s:%d/sources/%s", queryGroupName(namespace), namespace, defaultQueryServicePort, name)
	}
	return fmt.Sprintf("http://%s-query.%s:%d", name, namespace, port)
}
