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

// ragsource_search_tool.go offers each RAGSource's reference text as a tool. The query
// service is also an MCP server (path /mcp) with one tool, <ragsource>_search. The
// operator registers it the way every tool server reaches agents: it generates an
// MCPServer that points at the query service, the crew's MCPGateway selects it by label,
// and an agent lists <ragsource>_search in spec.enabledTools. An agent that lists the
// RAGSource in spec.ragSources instead is searched up front, as before.
package controller

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// labelGeneratedBy marks an object the operator generated from another object.
	labelGeneratedBy = "kubemoot.ai/generated-by"
	// generatedByRAGSearch is the labelGeneratedBy value on a RAGSource's search MCPServer.
	generatedByRAGSearch = "rag-search"
	// ragSearchSuffix names the generated MCPServer: <ragsource>-search.
	ragSearchSuffix = "-search"
	// ragSearchPath is where the query service serves MCP.
	ragSearchPath = "/mcp"
)

// ragSearchServerName is the name of the MCPServer generated for a RAGSource.
func ragSearchServerName(ragSourceName string) string {
	return boundedName(ragSourceName + ragSearchSuffix)
}

// ragSearchEndpoint is the MCP endpoint of a RAGSource's query service.
func ragSearchEndpoint(ragSource *kubemootv1alpha1.RAGSource) string {
	return ragQueryEndpoint(ragSource.Name, ragSource.Namespace, queryServicePort(ragSource)) + ragSearchPath
}

// ragSearchOffered reports whether the operator generates a search MCPServer for the
// RAGSource: it has a query service, and a person or a crew chart made it. A RAGSource
// the operator made for its own use (the tool index, a crew's agent resumes) is not
// something an agent searches, so it gets no tool.
func ragSearchOffered(ragSource *kubemootv1alpha1.RAGSource) bool {
	return queryServiceEnabled(ragSource) && metav1.GetControllerOf(ragSource) == nil
}

// isHelmOrFluxLabel reports whether a label belongs to the tool that installed the
// RAGSource, or says who manages it. The generated MCPServer is not part of that release and
// the operator manages it, so copying them would misstate both. The managed-by-resource label
// is one crew charts stamp on their resources (value kubemoot) to say a Kubemoot resource
// manages them; the generated MCPServer's own managed-by label says the operator does.
func isHelmOrFluxLabel(key string) bool {
	return key == labelManagedBy ||
		strings.HasPrefix(key, "helm.sh/") ||
		strings.HasPrefix(key, "helm.toolkit.fluxcd.io/") ||
		strings.HasPrefix(key, "kustomize.toolkit.fluxcd.io/") ||
		key == "app.kubernetes.io/managed-by-resource"
}

// ragSearchLabels are the labels of the generated MCPServer. It carries the RAGSource's
// own labels (a crew chart labels every resource with the release instance, which is what
// the crew's MCPGateway selects tool servers by), except the ones the installing tool
// owns, plus the operator's.
func ragSearchLabels(ragSource *kubemootv1alpha1.RAGSource) map[string]string {
	labels := make(map[string]string, len(ragSource.Labels)+4)
	for key, value := range ragSource.Labels {
		if !isHelmOrFluxLabel(key) {
			labels[key] = value
		}
	}
	labels[labelName] = ragSearchServerName(ragSource.Name)
	labels[labelManagedBy] = managedByValue
	labels[labelGeneratedBy] = generatedByRAGSearch
	labels[labelRAGSource] = ragSource.Name
	return labels
}

// buildRAGSearchServer is the MCPServer that points the crew's gateway at the query service.
func buildRAGSearchServer(ragSource *kubemootv1alpha1.RAGSource) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ragSearchServerName(ragSource.Name),
			Namespace: ragSource.Namespace,
			Labels:    ragSearchLabels(ragSource),
		},
		Spec: kubemootv1alpha1.MCPServerSpec{
			ExternalEndpoint: ragSearchEndpoint(ragSource),
			Transport:        kubemootv1alpha1.TransportHTTP,
		},
	}
}

// ownsSearchServer reports whether the MCPServer is the one this RAGSource generated: it
// carries the generated-by label and this RAGSource is its controller.
func ownsSearchServer(server *kubemootv1alpha1.MCPServer, ragSource *kubemootv1alpha1.RAGSource) bool {
	return server.Labels[labelGeneratedBy] == generatedByRAGSearch && metav1.IsControlledBy(server, ragSource)
}

// reconcileSearchTool creates, updates, or removes the RAGSource's search MCPServer so it
// matches whether the RAGSource offers one.
func (r *RAGSourceReconciler) reconcileSearchTool(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) error {
	if ragSearchOffered(ragSource) {
		return r.ensureSearchTool(ctx, ragSource)
	}
	return r.removeSearchTool(ctx, ragSource)
}

// ensureSearchTool creates the search MCPServer or brings it back to what the RAGSource asks
// for. An MCPServer of the same name that the operator did not generate is left alone.
func (r *RAGSourceReconciler) ensureSearchTool(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) error {
	log := logf.FromContext(ctx)
	desired := buildRAGSearchServer(ragSource)
	if err := controllerutil.SetControllerReference(ragSource, desired, r.Scheme); err != nil {
		return err
	}

	current := &kubemootv1alpha1.MCPServer{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, current)
	if errors.IsNotFound(err) {
		log.Info("Creating search tool MCPServer", "mcpServer", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if !ownsSearchServer(current, ragSource) {
		// Either a person's MCPServer, or one generated for another RAGSource (two long
		// names can bound to one name). It is never taken over or rewritten.
		log.Info("An MCPServer this RAGSource's search tool would use already exists and is not its own; leaving it alone",
			"mcpServer", current.Name)
		return nil
	}
	if equality.Semantic.DeepEqual(current.Spec, desired.Spec) &&
		equality.Semantic.DeepEqual(current.Labels, desired.Labels) {
		return nil
	}
	log.Info("Updating search tool MCPServer", "mcpServer", desired.Name)
	current.Spec = desired.Spec
	current.Labels = desired.Labels
	return r.Update(ctx, current)
}

// removeSearchTool deletes the search MCPServer this RAGSource generated, if any.
func (r *RAGSourceReconciler) removeSearchTool(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) error {
	current := &kubemootv1alpha1.MCPServer{}
	key := types.NamespacedName{Name: ragSearchServerName(ragSource.Name), Namespace: ragSource.Namespace}
	if err := r.Get(ctx, key, current); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !ownsSearchServer(current, ragSource) {
		return nil
	}
	logf.FromContext(ctx).Info("Removing search tool MCPServer", "mcpServer", current.Name)
	return client.IgnoreNotFound(r.Delete(ctx, current))
}
