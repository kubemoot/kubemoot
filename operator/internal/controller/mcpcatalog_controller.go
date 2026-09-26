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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// MCPCatalogReconciler reconciles an MCPCatalog object
type MCPCatalogReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HTTPClient *http.Client
}

// OfficialRegistryServerWrapper represents a server entry from the official MCP registry
// The registry uses a nested structure with "server" and "_meta" fields
type OfficialRegistryServerWrapper struct {
	Server OfficialRegistryServer `json:"server"`
	Meta   struct {
		Official struct {
			Status      string `json:"status,omitempty"`
			PublishedAt string `json:"publishedAt,omitempty"`
		} `json:"io.modelcontextprotocol.registry/official,omitempty"`
	} `json:"_meta,omitempty"`
}

// OfficialRegistryServer represents the actual server data within the wrapper
type OfficialRegistryServer struct {
	Schema      string `json:"$schema,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Repository  struct {
		URL    string `json:"url,omitempty"`
		Source string `json:"source,omitempty"`
	} `json:"repository,omitempty"`
	Packages []OfficialRegistryPackage `json:"packages,omitempty"`
}

// OfficialRegistryPackage represents a package/distribution of the server
type OfficialRegistryPackage struct {
	RegistryType string `json:"registryType,omitempty"`
	Identifier   string `json:"identifier,omitempty"`
	Transport    struct {
		Type string `json:"type,omitempty"`
	} `json:"transport,omitempty"`
	EnvironmentVariables []struct {
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
		IsSecret    bool   `json:"isSecret,omitempty"`
	} `json:"environmentVariables,omitempty"`
}

// OfficialRegistryResponse represents the response from the official MCP registry
type OfficialRegistryResponse struct {
	Servers []OfficialRegistryServerWrapper `json:"servers"`
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpcatalogs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpcatalogs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpcatalogs/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpqualitypolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *MCPCatalogReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the MCPCatalog instance
	catalog := &aiv1alpha1.MCPCatalog{}
	if err := r.Get(ctx, req.NamespacedName, catalog); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling MCPCatalog", "name", catalog.Name, "type", catalog.Spec.Type)

	// Sync the catalog based on type
	switch catalog.Spec.Type {
	case aiv1alpha1.CatalogTypeOfficialRegistry:
		return r.syncOfficialRegistry(ctx, catalog)
	case aiv1alpha1.CatalogTypeSmithery:
		return r.syncSmithery(ctx, catalog)
	case aiv1alpha1.CatalogTypeGlama:
		return r.syncGlama(ctx, catalog)
	case aiv1alpha1.CatalogTypeDocker:
		return r.syncDocker(ctx, catalog)
	case aiv1alpha1.CatalogTypeNpm:
		return r.syncNpm(ctx, catalog)
	case aiv1alpha1.CatalogTypeAgent:
		return r.syncWithAgent(ctx, catalog)
	default:
		log.Error(nil, "Unknown catalog type", "type", catalog.Spec.Type)
		return r.updateStatus(ctx, catalog, "Error", fmt.Sprintf("Unknown catalog type: %s", catalog.Spec.Type), nil)
	}
}

// syncOfficialRegistry syncs from the official MCP registry
// https://registry.modelcontextprotocol.io/v0/servers
func (r *MCPCatalogReconciler) syncOfficialRegistry(ctx context.Context, catalog *aiv1alpha1.MCPCatalog) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	httpClient := r.getHTTPClient()
	resp, err := httpClient.Get(catalog.Spec.URL)
	if err != nil {
		log.Error(err, "Failed to fetch official registry", "url", catalog.Spec.URL)
		return r.updateStatus(ctx, catalog, "Error", fmt.Sprintf("Failed to fetch registry: %v", err), nil)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return r.updateStatus(ctx, catalog, "Error", fmt.Sprintf("Registry returned status %d", resp.StatusCode), nil)
	}

	var registryResp OfficialRegistryResponse
	if err := json.NewDecoder(resp.Body).Decode(&registryResp); err != nil {
		log.Error(err, "Failed to parse registry response")
		return r.updateStatus(ctx, catalog, "Error", "Failed to parse registry response", nil)
	}

	// Convert to DiscoveredServer format
	discoveredServers := r.convertOfficialServers(registryResp.Servers, catalog)

	// Apply quality policy filtering if configured
	var allowedCount, blockedCount int
	if catalog.Spec.QualityPolicyRef != "" {
		discoveredServers, allowedCount, blockedCount = r.applyQualityPolicy(ctx, catalog, discoveredServers)
	} else {
		allowedCount = len(discoveredServers)
	}

	log.Info("Synced official registry",
		"discovered", len(registryResp.Servers),
		"allowed", allowedCount,
		"blocked", blockedCount)

	return r.updateStatusWithServers(ctx, catalog, catalogStatusUpdate{
		phase:      "Ready",
		message:    "Synced from official registry",
		servers:    discoveredServers,
		discovered: len(registryResp.Servers),
		allowed:    allowedCount,
		blocked:    blockedCount,
	})
}

// convertOfficialServers converts official registry servers to DiscoveredServer format
func (r *MCPCatalogReconciler) convertOfficialServers(wrappers []OfficialRegistryServerWrapper, catalog *aiv1alpha1.MCPCatalog) []aiv1alpha1.DiscoveredServer {
	var discovered []aiv1alpha1.DiscoveredServer

	maxServers := catalog.Spec.MaxServers
	if maxServers == 0 {
		maxServers = 100 // default
	}

	for i, wrapper := range wrappers {
		if i >= maxServers {
			break
		}

		server := wrapper.Server

		// Skip servers without deployable packages
		if len(server.Packages) == 0 {
			continue
		}

		// Filter by queries if specified
		if len(catalog.Spec.Queries) > 0 && !r.matchesQueries(server, catalog.Spec.Queries) {
			continue
		}

		pkg := server.Packages[0]
		ds := aiv1alpha1.DiscoveredServer{
			Name:              server.Name,
			Description:       server.Description,
			Version:           server.Version,
			GitHubURL:         server.Repository.URL,
			RegistryType:      pkg.RegistryType,
			PackageIdentifier: pkg.Identifier,
			Transport:         pkg.Transport.Type,
		}

		discovered = append(discovered, ds)
	}

	return discovered
}

// matchesQueries checks if a server matches the query filters
func (r *MCPCatalogReconciler) matchesQueries(server OfficialRegistryServer, queries []string) bool {
	for _, query := range queries {
		// Check name/description contains query (case-insensitive)
		if containsIgnoreCase(server.Name, query) || containsIgnoreCase(server.Description, query) {
			return true
		}
	}
	return false
}

// syncSmithery syncs from Smithery catalog
func (r *MCPCatalogReconciler) syncSmithery(ctx context.Context, catalog *aiv1alpha1.MCPCatalog) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Smithery catalog sync not yet implemented", "url", catalog.Spec.URL)

	// TODO: Implement Smithery API integration
	return r.updateStatus(ctx, catalog, "Pending", "Smithery sync not yet implemented", nil)
}

// syncGlama syncs from Glama directory
func (r *MCPCatalogReconciler) syncGlama(ctx context.Context, catalog *aiv1alpha1.MCPCatalog) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Glama catalog sync not yet implemented", "url", catalog.Spec.URL)

	// TODO: Implement Glama API integration
	return r.updateStatus(ctx, catalog, "Pending", "Glama sync not yet implemented", nil)
}

// syncDocker syncs from Docker Hub mcp/ namespace
func (r *MCPCatalogReconciler) syncDocker(ctx context.Context, catalog *aiv1alpha1.MCPCatalog) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Docker catalog sync not yet implemented", "url", catalog.Spec.URL)

	// TODO: Implement Docker Hub API integration
	return r.updateStatus(ctx, catalog, "Pending", "Docker sync not yet implemented", nil)
}

// syncNpm syncs from npm @modelcontextprotocol packages
func (r *MCPCatalogReconciler) syncNpm(ctx context.Context, catalog *aiv1alpha1.MCPCatalog) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("npm catalog sync not yet implemented", "url", catalog.Spec.URL)

	// TODO: Implement npm registry API integration
	return r.updateStatus(ctx, catalog, "Pending", "npm sync not yet implemented", nil)
}

// syncWithAgent uses a ReAct agent to discover servers from non-standard catalogs
func (r *MCPCatalogReconciler) syncWithAgent(ctx context.Context, catalog *aiv1alpha1.MCPCatalog) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if catalog.Spec.AgentRef == "" {
		return r.updateStatus(ctx, catalog, "Error", "AgentRef is required for agent catalog type", nil)
	}

	// Verify the referenced Agent exists and is ready
	agent := &aiv1alpha1.Agent{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: catalog.Namespace,
		Name:      catalog.Spec.AgentRef,
	}, agent); err != nil {
		log.Error(err, "Failed to find referenced Agent", "agentRef", catalog.Spec.AgentRef)
		return r.updateStatus(ctx, catalog, "Error", fmt.Sprintf("Agent not found: %s", catalog.Spec.AgentRef), nil)
	}

	if !agent.Status.Ready || agent.Status.Endpoint == "" {
		log.Info("Agent not ready yet", "agent", catalog.Spec.AgentRef, "ready", agent.Status.Ready)
		return r.updateStatus(ctx, catalog, "Pending", fmt.Sprintf("Waiting for agent %s to be ready", catalog.Spec.AgentRef), nil)
	}

	// Build the discovery prompt
	prompt := fmt.Sprintf(`Discover MCP servers from this catalog URL: %s

Search for servers matching these queries: %v

Return the discovered servers as JSON.`, catalog.Spec.URL, catalog.Spec.Queries)

	// Invoke the agent
	response, err := r.invokeAgent(ctx, agent.Status.Endpoint, prompt)
	if err != nil {
		log.Error(err, "Failed to invoke discovery agent")
		return r.updateStatus(ctx, catalog, "Error", fmt.Sprintf("Agent invocation failed: %v", err), nil)
	}

	// Parse the agent's response
	discoveredServers, err := r.parseAgentDiscoveryResponse(response)
	if err != nil {
		log.Error(err, "Failed to parse agent response", "response", response)
		return r.updateStatus(ctx, catalog, "Error", "Failed to parse agent discovery response", nil)
	}

	// Apply quality policy filtering if configured
	var allowedCount, blockedCount int
	if catalog.Spec.QualityPolicyRef != "" {
		discoveredServers, allowedCount, blockedCount = r.applyQualityPolicy(ctx, catalog, discoveredServers)
	} else {
		allowedCount = len(discoveredServers)
	}

	log.Info("Agent-based catalog sync complete",
		"url", catalog.Spec.URL,
		"discovered", len(discoveredServers)+blockedCount,
		"allowed", allowedCount,
		"blocked", blockedCount)

	return r.updateStatusWithServers(ctx, catalog, catalogStatusUpdate{
		phase:      "Ready",
		message:    "Synced via discovery agent",
		servers:    discoveredServers,
		discovered: len(discoveredServers) + blockedCount,
		allowed:    allowedCount,
		blocked:    blockedCount,
	})
}

// invokeAgent sends a chat request to an Agent and returns the response
func (r *MCPCatalogReconciler) invokeAgent(ctx context.Context, endpoint, message string) (string, error) {
	reqBody := map[string]interface{}{
		"message": message,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/chat", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := r.getHTTPClient()
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to invoke agent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent returned status %d", resp.StatusCode)
	}

	var respBody struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return respBody.Response, nil
}

// AgentDiscoveryResponse represents the structured response from the discovery agent
type AgentDiscoveryResponse struct {
	Servers        []AgentDiscoveredServer `json:"servers"`
	DiscoveryNotes string                  `json:"discoveryNotes,omitempty"`
}

// AgentDiscoveredServer represents a server discovered by the agent
type AgentDiscoveredServer struct {
	Name              string   `json:"name"`
	Description       string   `json:"description,omitempty"`
	Version           string   `json:"version,omitempty"`
	Author            string   `json:"author,omitempty"`
	GitHubURL         string   `json:"githubUrl,omitempty"`
	RegistryType      string   `json:"registryType,omitempty"`
	PackageIdentifier string   `json:"packageIdentifier,omitempty"`
	Transport         string   `json:"transport,omitempty"`
	Categories        []string `json:"categories,omitempty"`
}

// parseAgentDiscoveryResponse extracts DiscoveredServer list from agent response
func (r *MCPCatalogReconciler) parseAgentDiscoveryResponse(response string) ([]aiv1alpha1.DiscoveredServer, error) {
	// Try to extract JSON from the response (agent may include ReAct reasoning before JSON)
	jsonStart := strings.Index(response, "{")
	if jsonStart == -1 {
		return nil, fmt.Errorf("no JSON found in agent response")
	}
	jsonStr := response[jsonStart:]

	// Find the end of JSON (handle nested objects)
	var agentResp AgentDiscoveryResponse
	if err := json.Unmarshal([]byte(jsonStr), &agentResp); err != nil {
		// Try to find a simpler array format
		arrayStart := strings.Index(response, "[")
		if arrayStart != -1 {
			var servers []AgentDiscoveredServer
			if err := json.Unmarshal([]byte(response[arrayStart:]), &servers); err != nil {
				return nil, fmt.Errorf("failed to parse agent response: %w", err)
			}
			agentResp.Servers = servers
		} else {
			return nil, fmt.Errorf("failed to parse agent response: %w", err)
		}
	}

	// Convert to DiscoveredServer format
	var discovered []aiv1alpha1.DiscoveredServer
	for _, s := range agentResp.Servers {
		discovered = append(discovered, aiv1alpha1.DiscoveredServer{
			Name:              s.Name,
			Description:       s.Description,
			Version:           s.Version,
			Author:            s.Author,
			GitHubURL:         s.GitHubURL,
			RegistryType:      s.RegistryType,
			PackageIdentifier: s.PackageIdentifier,
			Transport:         s.Transport,
			Categories:        s.Categories,
		})
	}

	return discovered, nil
}

// applyQualityPolicy filters discovered servers through the quality policy
func (r *MCPCatalogReconciler) applyQualityPolicy(ctx context.Context, catalog *aiv1alpha1.MCPCatalog, servers []aiv1alpha1.DiscoveredServer) ([]aiv1alpha1.DiscoveredServer, int, int) {
	log := logf.FromContext(ctx)

	// Fetch the quality policy
	policy := &aiv1alpha1.MCPQualityPolicy{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: catalog.Namespace,
		Name:      catalog.Spec.QualityPolicyRef,
	}, policy); err != nil {
		log.Error(err, "Failed to find quality policy", "policyRef", catalog.Spec.QualityPolicyRef)
		// Return all servers if policy not found
		return servers, len(servers), 0
	}

	var allowed []aiv1alpha1.DiscoveredServer
	var blockedCount int

	for _, server := range servers {
		decision := r.evaluateServerAgainstPolicy(ctx, policy, server)
		server.QualityDecision = decision.Action
		server.QualityReason = decision.Reason

		if decision.Action == "allow" {
			allowed = append(allowed, server)
		} else {
			blockedCount++
		}
	}

	return allowed, len(allowed), blockedCount
}

// evaluateServerAgainstPolicy checks a server against the quality policy
func (r *MCPCatalogReconciler) evaluateServerAgainstPolicy(ctx context.Context, policy *aiv1alpha1.MCPQualityPolicy, server aiv1alpha1.DiscoveredServer) PolicyDecision {
	// Check allowing list first
	if decision, matched := r.checkAllowingList(policy, server); matched {
		return decision
	}

	// Check blocking list
	if decision, matched := r.checkBlockingList(policy, server); matched {
		return decision
	}

	// Check tested tier (MCPServerReport data)
	if policy.Spec.Tested != nil && policy.Spec.Tested.Enabled {
		decision := r.evaluateTestedTier(ctx, policy, server)
		if decision.Action != "" {
			return decision
		}
		// Empty action means no report found or caution — fall through to considering
	}

	return r.evaluateConsideringTier(ctx, policy, server)
}

// checkAllowingList returns an allow decision if the server matches the policy allowing list.
func (r *MCPCatalogReconciler) checkAllowingList(policy *aiv1alpha1.MCPQualityPolicy, server aiv1alpha1.DiscoveredServer) (PolicyDecision, bool) {
	for _, entry := range policy.Spec.Allowing {
		if entry.Name != "" && entry.Name == server.Name {
			return PolicyDecision{Action: "allow", Confidence: 1.0, Reason: "In allowing list (by name)"}, true
		}
		if entry.Author != "" && entry.Author == server.Author {
			return PolicyDecision{Action: "allow", Confidence: 1.0, Reason: "In allowing list (by author)"}, true
		}
	}
	return PolicyDecision{}, false
}

// checkBlockingList returns a deny decision if the server matches the policy blocking list.
func (r *MCPCatalogReconciler) checkBlockingList(policy *aiv1alpha1.MCPQualityPolicy, server aiv1alpha1.DiscoveredServer) (PolicyDecision, bool) {
	for _, entry := range policy.Spec.Blocking {
		if r.matchesBlockingEntryForDiscoveredServer(entry, server) {
			reason := "Matched blocking rule"
			if entry.Reason != "" {
				reason = entry.Reason
			}
			return PolicyDecision{Action: "deny", Confidence: 1.0, Reason: reason}, true
		}
	}
	return PolicyDecision{}, false
}

// evaluateConsideringTier handles AI evaluation or fallback for the considering tier.
func (r *MCPCatalogReconciler) evaluateConsideringTier(ctx context.Context, policy *aiv1alpha1.MCPQualityPolicy, server aiv1alpha1.DiscoveredServer) PolicyDecision {
	log := logf.FromContext(ctx)

	if policy.Spec.Considering == nil {
		return PolicyDecision{Action: "allow", Confidence: 0.5, Reason: "No policy matched, default allow"}
	}

	if !policy.Spec.Considering.Enabled {
		return PolicyDecision{
			Action:     policy.Spec.Considering.FallbackAction,
			Confidence: 1.0,
			Reason:     "AI evaluation disabled, using fallback",
		}
	}

	metrics := r.fetchGitHubMetrics(ctx, server.GitHubURL)
	decision, err := r.invokeQualityEvaluator(ctx, policy, server, metrics)
	if err != nil {
		log.Error(err, "Failed to invoke quality evaluator, using fallback", "server", server.Name)
		return PolicyDecision{
			Action:     policy.Spec.Considering.FallbackAction,
			Confidence: 0.5,
			Reason:     fmt.Sprintf("AI evaluation failed: %v, using fallback", err),
		}
	}
	return decision
}

// evaluateTestedTier checks MCPServerReport for prior test experience
func (r *MCPCatalogReconciler) evaluateTestedTier(ctx context.Context, policy *aiv1alpha1.MCPQualityPolicy, server aiv1alpha1.DiscoveredServer) PolicyDecision {
	log := logf.FromContext(ctx)

	// Look up MCPServerReport by sanitized server name
	reportName := sanitizeK8sName(server.Name)
	report := &aiv1alpha1.MCPServerReport{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: policy.Namespace,
		Name:      reportName,
	}, report); err != nil {
		// No report found — pass through to next tier
		return PolicyDecision{}
	}

	tested := policy.Spec.Tested

	// Block servers with "avoid" verdict
	if tested.BlockBroken && report.Status.Verdict == string(aiv1alpha1.VerdictAvoid) {
		log.Info("Blocking server with avoid verdict", "server", server.Name)
		return PolicyDecision{
			Action:     "deny",
			Confidence: 1.0,
			Reason:     fmt.Sprintf("tested: verdict=avoid, %d failures recorded", report.Status.FailureCount),
		}
	}

	// Auto-allow servers with "use" verdict and sufficient success rate
	if report.Status.Verdict == string(aiv1alpha1.VerdictUse) {
		minRate := 0.8
		if tested.MinSuccessRate != "" {
			if parsed, err := parseFloat(tested.MinSuccessRate); err == nil {
				minRate = parsed
			}
		}
		// Parse success rate from status (format: "85%")
		var actualRate float64
		if report.Status.SuccessRate != "N/A" {
			fmt.Sscanf(report.Status.SuccessRate, "%f%%", &actualRate)
			actualRate /= 100
		}
		if actualRate >= minRate {
			return PolicyDecision{
				Action:     "allow",
				Confidence: 0.95,
				Reason:     fmt.Sprintf("tested: verdict=use, success rate %s", report.Status.SuccessRate),
			}
		}
	}

	// Caution or insufficient data — fall through to considering with context
	return PolicyDecision{}
}

// GitHubMetrics contains metrics fetched from GitHub API
type GitHubMetrics struct {
	Stars          int    `json:"stars"`
	Forks          int    `json:"forks"`
	LastCommitDate string `json:"last_commit_date"`
	OpenIssues     int    `json:"open_issues"`
	License        string `json:"license,omitempty"`
	Error          string `json:"error,omitempty"`
}

// fetchGitHubMetrics fetches repository metrics from GitHub API
func (r *MCPCatalogReconciler) fetchGitHubMetrics(ctx context.Context, githubURL string) GitHubMetrics {
	log := logf.FromContext(ctx)
	metrics := GitHubMetrics{}

	if githubURL == "" {
		metrics.Error = "no GitHub URL provided"
		return metrics
	}

	// Extract owner/repo from GitHub URL
	// Handles: https://github.com/owner/repo or github.com/owner/repo
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(githubURL, "https://"), "http://"), "/")
	if len(parts) < 3 || parts[0] != "github.com" {
		metrics.Error = "invalid GitHub URL format"
		return metrics
	}
	owner := parts[1]
	repo := strings.TrimSuffix(parts[2], ".git")

	// Fetch from GitHub API
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		metrics.Error = fmt.Sprintf("failed to create request: %v", err)
		return metrics
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Kubemoot-Operator/1.0")

	httpClient := r.getHTTPClient()
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Error(err, "Failed to fetch GitHub metrics", "url", apiURL)
		metrics.Error = fmt.Sprintf("failed to fetch: %v", err)
		return metrics
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		metrics.Error = fmt.Sprintf("GitHub API returned status %d", resp.StatusCode)
		return metrics
	}

	var repoData struct {
		StargazersCount int    `json:"stargazers_count"`
		ForksCount      int    `json:"forks_count"`
		OpenIssuesCount int    `json:"open_issues_count"`
		PushedAt        string `json:"pushed_at"`
		License         struct {
			Name string `json:"name"`
		} `json:"license"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&repoData); err != nil {
		metrics.Error = fmt.Sprintf("failed to decode response: %v", err)
		return metrics
	}

	metrics.Stars = repoData.StargazersCount
	metrics.Forks = repoData.ForksCount
	metrics.OpenIssues = repoData.OpenIssuesCount
	metrics.LastCommitDate = repoData.PushedAt
	metrics.License = repoData.License.Name

	return metrics
}

// invokeQualityEvaluator calls the Quality Evaluator Agent to assess a server
func (r *MCPCatalogReconciler) invokeQualityEvaluator(ctx context.Context, policy *aiv1alpha1.MCPQualityPolicy, server aiv1alpha1.DiscoveredServer, metrics GitHubMetrics) (PolicyDecision, error) {
	log := logf.FromContext(ctx)

	// Find the Quality Evaluator Agent
	agent := &aiv1alpha1.Agent{}
	agentKey := client.ObjectKey{
		Namespace: policy.Namespace,
		Name:      policy.Spec.Considering.AgentRef,
	}

	// First try same namespace as policy
	if err := r.Get(ctx, agentKey, agent); err != nil {
		// Try kubemoot-system namespace (internal agents)
		agentKey.Namespace = "kubemoot-system"
		if err := r.Get(ctx, agentKey, agent); err != nil {
			return PolicyDecision{}, fmt.Errorf("quality evaluator agent not found: %s", policy.Spec.Considering.AgentRef)
		}
	}

	if !agent.Status.Ready || agent.Status.Endpoint == "" {
		return PolicyDecision{}, fmt.Errorf("quality evaluator agent not ready")
	}

	// Build the evaluation request with server metadata and metrics
	evalRequest := map[string]interface{}{
		"name":        server.Name,
		"author":      server.Author,
		"description": server.Description,
		"version":     server.Version,
		"categories":  server.Categories,
		"github_url":  server.GitHubURL,
		"github_metrics": map[string]interface{}{
			"stars":            metrics.Stars,
			"forks":            metrics.Forks,
			"last_commit_date": metrics.LastCommitDate,
			"open_issues":      metrics.OpenIssues,
			"license":          metrics.License,
			"error":            metrics.Error,
		},
	}

	requestJSON, err := json.Marshal(evalRequest)
	if err != nil {
		return PolicyDecision{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	prompt := fmt.Sprintf("Evaluate this MCP server for quality and trust:\n\n%s", string(requestJSON))

	// Invoke the agent
	response, err := r.invokeAgent(ctx, agent.Status.Endpoint, prompt)
	if err != nil {
		return PolicyDecision{}, fmt.Errorf("failed to invoke agent: %w", err)
	}

	// Parse the agent's response
	decision, err := r.parseQualityEvaluatorResponse(response)
	if err != nil {
		log.Error(err, "Failed to parse quality evaluator response", "response", response)
		return PolicyDecision{}, fmt.Errorf("failed to parse response: %w", err)
	}

	return decision, nil
}

// parseQualityEvaluatorResponse parses the Quality Evaluator Agent's response
func (r *MCPCatalogReconciler) parseQualityEvaluatorResponse(response string) (PolicyDecision, error) {
	// Try to extract JSON from the response (agent may include ReAct reasoning before JSON)
	jsonStart := strings.Index(response, "{")
	if jsonStart == -1 {
		return PolicyDecision{}, fmt.Errorf("no JSON found in response")
	}

	// Find matching closing brace
	jsonStr := response[jsonStart:]
	braceCount := 0
	jsonEnd := -1
	for i, ch := range jsonStr {
		if ch == '{' {
			braceCount++
		} else if ch == '}' {
			braceCount--
			if braceCount == 0 {
				jsonEnd = i + 1
				break
			}
		}
	}
	if jsonEnd == -1 {
		return PolicyDecision{}, fmt.Errorf("malformed JSON in response")
	}
	jsonStr = jsonStr[:jsonEnd]

	var evalResp struct {
		Decision   string  `json:"decision"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &evalResp); err != nil {
		return PolicyDecision{}, fmt.Errorf("failed to parse JSON: %w", err)
	}

	action := "deny"
	if strings.ToLower(evalResp.Decision) == "allow" {
		action = "allow"
	}

	return PolicyDecision{
		Action:     action,
		Confidence: evalResp.Confidence,
		Reason:     evalResp.Reason,
	}, nil
}

// matchesBlockingEntryForDiscoveredServer checks if a server matches a blocking entry
func (r *MCPCatalogReconciler) matchesBlockingEntryForDiscoveredServer(entry aiv1alpha1.BlockingEntry, server aiv1alpha1.DiscoveredServer) bool {
	// Check name matcher
	if entry.Name != nil {
		if !matchStringMatcher(*entry.Name, server.Name) {
			return false
		}
	}

	// Check author matcher
	if entry.Author != nil {
		if !matchStringMatcher(*entry.Author, server.Author) {
			return false
		}
	}

	// Check version constraint
	if entry.Version != "" {
		if !matchVersionConstraint(entry.Version, server.Version) {
			return false
		}
	}

	// If all specified matchers pass, it's a match
	return entry.Name != nil || entry.Author != nil || entry.Version != ""
}

// catalogStatusUpdate groups the fields written to an MCPCatalog status during a
// reconcile. It keeps updateStatusWithServers to a small, named parameter set.
type catalogStatusUpdate struct {
	phase      string
	message    string
	servers    []aiv1alpha1.DiscoveredServer
	discovered int
	allowed    int
	blocked    int
}

// updateStatus updates the MCPCatalog status
func (r *MCPCatalogReconciler) updateStatus(ctx context.Context, catalog *aiv1alpha1.MCPCatalog, phase, message string, servers []aiv1alpha1.DiscoveredServer) (ctrl.Result, error) {
	return r.updateStatusWithServers(ctx, catalog, catalogStatusUpdate{phase: phase, message: message, servers: servers})
}

// updateStatusWithServers updates the MCPCatalog status with server counts
func (r *MCPCatalogReconciler) updateStatusWithServers(ctx context.Context, catalog *aiv1alpha1.MCPCatalog, update catalogStatusUpdate) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	phase := update.phase
	message := update.message
	servers := update.servers

	catalog.Status.Phase = phase
	catalog.Status.Message = message
	catalog.Status.ServersDiscovered = update.discovered
	catalog.Status.ServersAllowed = update.allowed
	catalog.Status.ServersBlocked = update.blocked

	if servers != nil {
		// Store up to 50 servers in status (summary)
		if len(servers) > 50 {
			catalog.Status.DiscoveredServers = servers[:50]
		} else {
			catalog.Status.DiscoveredServers = servers
		}
	}

	now := metav1.Now()
	catalog.Status.LastSync = &now

	// Calculate next sync time
	syncInterval, err := time.ParseDuration(catalog.Spec.SyncInterval)
	if err != nil {
		syncInterval = 24 * time.Hour // default
	}
	nextSync := metav1.NewTime(time.Now().Add(syncInterval))
	catalog.Status.NextSync = &nextSync

	// Set condition
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if phase == "Ready" {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&catalog.Status.Conditions, condition)

	if err := r.Status().Update(ctx, catalog); err != nil {
		log.Error(err, "Failed to update MCPCatalog status")
		return ctrl.Result{}, err
	}

	// Requeue for next sync
	requeueAfter := syncInterval
	if phase != "Ready" {
		requeueAfter = 1 * time.Minute // Retry faster if not ready
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// getHTTPClient returns the HTTP client to use
func (r *MCPCatalogReconciler) getHTTPClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPCatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.MCPCatalog{}).
		Named("mcpcatalog").
		Complete(r)
}

// ============================================================================
// Helper Functions
// ============================================================================

// containsIgnoreCase checks if str contains substr (case-insensitive)
func containsIgnoreCase(str, substr string) bool {
	return strings.Contains(strings.ToLower(str), strings.ToLower(substr))
}

// matchStringMatcher handles exact, glob, and regex matching for StringMatcher
func matchStringMatcher(m aiv1alpha1.StringMatcher, value string) bool {
	var matched bool
	switch m.Type {
	case aiv1alpha1.MatcherTypeExact, "":
		matched = strings.EqualFold(value, m.Value)
	case aiv1alpha1.MatcherTypeGlob:
		// Simple glob matching using filepath.Match (supports *, ?)
		matched, _ = matchGlob(strings.ToLower(m.Value), strings.ToLower(value))
	case aiv1alpha1.MatcherTypeRegex:
		re, err := compileRegex(m.Value)
		if err != nil {
			return false
		}
		matched = re.MatchString(value)
	}
	if m.Negate {
		return !matched
	}
	return matched
}

// matchVersionConstraint checks if version satisfies the constraint.
// Delegates to the shared matchVersionConstraintShared function.
func matchVersionConstraint(constraint, version string) bool {
	return matchVersionConstraintShared(constraint, version)
}
