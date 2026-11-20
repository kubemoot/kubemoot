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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The tool smoke is Layer 1 of the crew fitness layers: before any model runs, list
// the tools of every MCPServer in the suite's namespace through the crew's MCP gateway
// (GET /tools) and call each tool that declares a safe input (POST /tools/{tool}/call),
// the same REST path agents use. No model is called.
const (
	// toolSmokeDefaultTimeout bounds one tool call unless its input sets a timeout.
	toolSmokeDefaultTimeout = 5 * time.Second
	// toolSmokeListTimeout bounds the tool listing.
	toolSmokeListTimeout = 10 * time.Second
	// toolSmokeParallelism caps the calls in flight, so a crew of many slow tools
	// holds the reconcile for the slowest few rather than their sum.
	toolSmokeParallelism = 8
	// toolSmokeMaxTimeout caps a declared call timeout, and toolSmokeTotalTimeout
	// bounds the whole smoke, so no declaration can pin a reconcile for long.
	toolSmokeMaxTimeout   = 60 * time.Second
	toolSmokeTotalTimeout = 3 * time.Minute
	// toolSmokeListRow is the tool column of a failure that belongs to the server
	// or the gateway rather than to one tool.
	toolSmokeListRow = "tools/list"
	// toolSmokeMaxBody bounds how much of a gateway reply the smoke reads.
	toolSmokeMaxBody = 1 << 20
)

// gatewayTool is one entry of the gateway's tool list.
type gatewayTool struct {
	Name       string `json:"name"`
	ServerName string `json:"serverName"`
}

// smokeCall is a tool call to make; a smokeItem without one is a result already settled.
type smokeCall struct {
	tool    string
	args    map[string]any
	timeout time.Duration
}

// smokeItem is one row of the plan: its result so far, and the call that completes it.
// toolSmokeRow is the API row type, aliased so the table code reads short.
type toolSmokeRow = kubemootv1alpha1.ToolSmokeResult

type smokeItem struct {
	result toolSmokeRow
	call   *smokeCall
}

// toolSmokeHTTPClient returns the client the smoke calls the gateway with.
func (r *CrewFitnessSuiteReconciler) toolSmokeHTTPClient() *http.Client {
	if r.smokeClient != nil {
		return r.smokeClient
	}
	return &http.Client{}
}

// toolSmokeGate runs the tool smoke at the start of a suite and records its table in
// status. It returns stop=true when a tool failed and the suite has been ended in
// phase ToolSmokeFailed; the caller then returns result and err as they are.
func (r *CrewFitnessSuiteReconciler) toolSmokeGate(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (result ctrl.Result, stop bool, err error) {
	if suite.Spec.SkipToolSmoke {
		setToolSmokeCondition(suite, metav1.ConditionTrue, kubemootv1alpha1.ToolSmokeReasonSkipped, "spec.skipToolSmoke is true")
		suite.Status.ToolSmoke = nil
		return ctrl.Result{}, false, nil
	}
	if toolSmokeRecorded(suite) {
		return ctrl.Result{}, false, nil
	}
	smokeCtx, cancel := context.WithTimeout(ctx, toolSmokeTotalTimeout)
	defer cancel()
	results, err := r.smokeCrewTools(smokeCtx, suite.Namespace)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	table := buildToolSmokeStatus(results)
	suite.Status.ToolSmoke = table
	switch {
	case !table.Passed:
		return r.endSuiteToolSmokeFailed(ctx, suite, table)
	case len(results) == 0:
		setToolSmokeCondition(suite, metav1.ConditionTrue, kubemootv1alpha1.ToolSmokeReasonNoServers, "the namespace has no MCPServer to smoke")
	default:
		setToolSmokeCondition(suite, metav1.ConditionTrue, kubemootv1alpha1.ToolSmokeReasonPassed,
			fmt.Sprintf("%d tools checked, none failed", len(results)))
	}
	// Record the outcome now, so a retry of the start (a conflict on the later
	// status update) does not call the tools a second time.
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, false, err
	}
	return ctrl.Result{}, false, nil
}

// toolSmokeRecorded reports whether the smoke already ended for this generation of
// the suite: a passed smoke is recorded in status before the suite starts, so a
// retried start skips it.
func toolSmokeRecorded(suite *kubemootv1alpha1.CrewFitnessSuite) bool {
	c := meta.FindStatusCondition(suite.Status.Conditions, kubemootv1alpha1.ConditionToolSmoke)
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == suite.Generation
}

// endSuiteToolSmokeFailed ends the suite before any scenario: terminal phase, the
// condition naming the first failures, and no artifact or child to reap.
func (r *CrewFitnessSuiteReconciler) endSuiteToolSmokeFailed(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, table *kubemootv1alpha1.ToolSmokeStatus) (ctrl.Result, bool, error) {
	now := metav1.Now()
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseToolSmokeFailed
	suite.Status.CompletedAt = &now
	setToolSmokeCondition(suite, metav1.ConditionFalse, kubemootv1alpha1.ToolSmokeReasonFailed, toolSmokeFailureMessage(table))
	logf.FromContext(ctx).Info("Tool smoke failed; suite stopped before any model ran",
		"suite", suite.Name, "failures", table.Failures)
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, true, nil
}

// smokeCrewTools runs the smoke over the MCPServers of the namespace through the
// namespace's crew gateway. A namespace with no MCPServer has nothing to smoke.
func (r *CrewFitnessSuiteReconciler) smokeCrewTools(ctx context.Context, namespace string) ([]toolSmokeRow, error) {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, servers, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing MCPServers for the tool smoke: %w", err)
	}
	if len(servers.Items) == 0 {
		return nil, nil
	}
	gateways := &kubemootv1alpha1.MCPGatewayList{}
	if err := r.List(ctx, gateways, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing MCPGateways for the tool smoke: %w", err)
	}
	gw := selectCrewGateway(gateways.Items)
	if gw == nil {
		return failEachServer(servers.Items, "no MCPGateway in the namespace to list its tools through"), nil
	}
	base := fmt.Sprintf("http://%s.%s.svc:%d", gw.Name, gw.Namespace, gatewayToolPort(gw))
	return runToolSmoke(ctx, r.toolSmokeHTTPClient(), base, servers.Items), nil
}

// failEachServer returns one tools/list failure per server, sorted by server name.
func failEachServer(servers []kubemootv1alpha1.MCPServer, reason string) []toolSmokeRow {
	rows := make([]toolSmokeRow, 0, len(servers))
	for i := range servers {
		rows = append(rows, toolSmokeRow{Server: servers[i].Name, Tool: toolSmokeListRow, Error: smokeText(reason, 400)})
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].Server < rows[b].Server })
	return rows
}

// runToolSmoke lists the gateway's tools at base, plans one row per tool of each
// server, and makes the declared calls. No model is involved.
func runToolSmoke(ctx context.Context, hc *http.Client, base string, servers []kubemootv1alpha1.MCPServer) []toolSmokeRow {
	listed, err := listGatewayTools(ctx, hc, base)
	if err != nil {
		return failEachServer(servers, "tool listing through the gateway failed: "+err.Error())
	}
	items := planToolSmoke(servers, listed)
	executeSmokeCalls(ctx, hc, base, items)
	rows := make([]toolSmokeRow, len(items))
	for i := range items {
		rows[i] = items[i].result
	}
	return rows
}

// doGatewayRequest sends req within timeout and returns the status code, status
// text and body (at most toolSmokeMaxBody). A deadline hit reads "no answer within".
func doGatewayRequest(ctx context.Context, hc *http.Client, req func(context.Context) (*http.Request, error), timeout time.Duration) (code int, status string, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := req(ctx)
	if err != nil {
		return 0, "", nil, err
	}
	resp, err := hc.Do(r)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		body, err = io.ReadAll(io.LimitReader(resp.Body, toolSmokeMaxBody))
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", nil, fmt.Errorf("no answer within %s", timeout)
		}
		return 0, "", nil, err
	}
	return resp.StatusCode, resp.Status, body, nil
}

// listGatewayTools reads GET /tools and groups the tool names by the server that
// registered them.
func listGatewayTools(ctx context.Context, hc *http.Client, base string) (map[string]map[string]bool, error) {
	code, status, body, err := doGatewayRequest(ctx, hc, func(c context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(c, http.MethodGet, base+"/tools", nil)
	}, toolSmokeListTimeout)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("GET /tools answered %s", status)
	}
	var list struct {
		Tools []gatewayTool `json:"tools"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("GET /tools reply is not a tool list: %w", err)
	}
	byServer := map[string]map[string]bool{}
	for _, t := range list.Tools {
		if byServer[t.ServerName] == nil {
			byServer[t.ServerName] = map[string]bool{}
		}
		byServer[t.ServerName][t.Name] = true
	}
	return byServer, nil
}

// planToolSmoke builds the rows: for each server by name, every tool it lists or
// declares an input for. A server that lists nothing fails once; a declared tool the
// server does not list fails; a listed tool with an input gets a call; a listed tool
// without one passes as listed.
func planToolSmoke(servers []kubemootv1alpha1.MCPServer, listed map[string]map[string]bool) []smokeItem {
	sorted := append([]kubemootv1alpha1.MCPServer(nil), servers...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].Name < sorted[b].Name })
	items := make([]smokeItem, 0, len(sorted))
	for i := range sorted {
		items = append(items, planServerSmoke(&sorted[i], listed[sorted[i].Name])...)
	}
	return items
}

func planServerSmoke(server *kubemootv1alpha1.MCPServer, tools map[string]bool) []smokeItem {
	if len(tools) == 0 {
		return []smokeItem{{result: toolSmokeRow{Server: server.Name, Tool: toolSmokeListRow,
			Error: fmt.Sprintf("the gateway lists no tools for this server (phase %q, ready %t: %s)",
				server.Status.Phase, server.Status.Ready, smokeText(server.Status.Message, 200))}}}
	}
	inputs := map[string]kubemootv1alpha1.ToolSmokeInput{}
	names := map[string]bool{}
	for _, in := range server.Spec.SmokeInputs {
		inputs[in.Name] = in
		names[in.Name] = true
	}
	for name := range tools {
		names[name] = true
	}
	order := make([]string, 0, len(names))
	for name := range names {
		order = append(order, name)
	}
	sort.Strings(order)
	items := make([]smokeItem, 0, len(order))
	for _, name := range order {
		items = append(items, planToolSmokeItem(server.Name, name, tools[name], inputs))
	}
	return items
}

// planToolSmokeItem settles one tool: listed-only, a call to make, or an unlisted
// declared tool.
func planToolSmokeItem(server, tool string, isListed bool, inputs map[string]kubemootv1alpha1.ToolSmokeInput) smokeItem {
	row := toolSmokeRow{Server: server, Tool: tool}
	in, declared := inputs[tool]
	switch {
	case !isListed:
		row.Error = "declared in spec.smokeInputs but the gateway does not list it for this server"
		return smokeItem{result: row}
	case !declared:
		row.Passed = true
		return smokeItem{result: row}
	}
	row.Invoked = true
	return smokeItem{result: row, call: newSmokeCall(in)}
}

// newSmokeCall turns a declared input into a call. Arguments go as text, as an
// agent's call sends them.
func newSmokeCall(in kubemootv1alpha1.ToolSmokeInput) *smokeCall {
	call := &smokeCall{tool: in.Name, args: map[string]any{}, timeout: toolSmokeDefaultTimeout}
	if in.Timeout != nil && in.Timeout.Duration > 0 {
		call.timeout = min(in.Timeout.Duration, toolSmokeMaxTimeout)
	}
	for k, v := range in.Arguments {
		call.args[k] = v
	}
	return call
}

// executeSmokeCalls makes every planned call, at most toolSmokeParallelism at a time,
// and fills the rows in place.
func executeSmokeCalls(ctx context.Context, hc *http.Client, base string, items []smokeItem) {
	sem := make(chan struct{}, toolSmokeParallelism)
	var wg sync.WaitGroup
	for i := range items {
		if items[i].call == nil {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			items[i].result.Error = "smoke deadline reached before this call started"
			continue
		}
		wg.Add(1)
		go func(it *smokeItem) {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			err := callGatewayTool(ctx, hc, base, it.call)
			it.result.DurationMs = time.Since(start).Milliseconds()
			it.result.Passed = err == nil
			if err != nil {
				it.result.Error = smokeText(err.Error(), 400)
			}
		}(&items[i])
	}
	wg.Wait()
}

// callGatewayTool calls POST /tools/{tool}/call and returns nil for a non-error
// answer within the call's timeout.
func callGatewayTool(ctx context.Context, hc *http.Client, base string, call *smokeCall) error {
	payload, err := json.Marshal(map[string]any{"arguments": call.args})
	if err != nil {
		return err
	}
	code, status, body, err := doGatewayRequest(ctx, hc, func(c context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(c, http.MethodPost,
			base+"/tools/"+url.PathEscape(call.tool)+"/call", bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	}, call.timeout)
	if err != nil {
		return err
	}
	return toolCallError(code, status, body)
}

// toolCallError reads the gateway's {"isError","content"} reply: nil when the call
// succeeded, otherwise the reason.
func toolCallError(code int, status string, body []byte) error {
	if code != http.StatusOK {
		return fmt.Errorf("gateway answered %s", status)
	}
	var reply struct {
		IsError bool   `json:"isError"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("reply is not a tool result: %v", err)
	}
	if reply.IsError {
		return fmt.Errorf("tool returned an error: %s", reply.Content)
	}
	return nil
}

// buildToolSmokeStatus turns rows into the status table: failures first (then by
// server and tool), capped at MaxToolSmokeResults.
func buildToolSmokeStatus(rows []toolSmokeRow) *kubemootv1alpha1.ToolSmokeStatus {
	sorted := append([]toolSmokeRow(nil), rows...)
	sort.SliceStable(sorted, func(a, b int) bool { return !sorted[a].Passed && sorted[b].Passed })
	failures := 0
	for i := range sorted {
		if !sorted[i].Passed {
			failures++
		}
	}
	if len(sorted) > kubemootv1alpha1.MaxToolSmokeResults {
		sorted = sorted[:kubemootv1alpha1.MaxToolSmokeResults]
	}
	return &kubemootv1alpha1.ToolSmokeStatus{Passed: failures == 0, Failures: int32(failures), Results: sorted}
}

// toolSmokeFailureMessage names up to three failures for the condition.
func toolSmokeFailureMessage(table *kubemootv1alpha1.ToolSmokeStatus) string {
	msg := fmt.Sprintf("%d tool(s) failed the smoke, no model ran", table.Failures)
	shown := 0
	for _, row := range table.Results {
		if row.Passed || shown == 3 {
			continue
		}
		msg += fmt.Sprintf("; %s/%s: %s", row.Server, row.Tool, smokeText(row.Error, 120))
		shown++
	}
	return msg
}

func setToolSmokeCondition(suite *kubemootv1alpha1.CrewFitnessSuite, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&suite.Status.Conditions, metav1.Condition{
		Type: kubemootv1alpha1.ConditionToolSmoke, Status: status, Reason: reason,
		Message: smokeText(message, 1000), ObservedGeneration: suite.Generation,
	})
}

// smokeText cuts s to at most n characters, ending in "..." when it cuts.
func smokeText(s string, n int) string {
	return truncateRunes(s, n, "...")
}
