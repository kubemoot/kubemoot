/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// The operator calibrates a provider when it needs it: it was never calibrated, a
// reset was requested, its engine changed, or Models were bound to it that the
// calibration has not measured. It asks once per need, and again for the same need
// only after the calibration paused as Waiting (crew calls were using the provider,
// or the server stopped) and the provider has since fallen idle, or after it Failed
// and the provider has since become ready again. It asks the accelerator-mcp core server,
// whose calibrate_provider tool runs the fixed recipe in the background and records
// the results on the provider; no model makes a decision in it.

const (
	calibrationResetAnnotation = "kubemoot.ai/calibration-reset"
	calibrationPhaseWaiting    = "Waiting"
	calibrationPhaseFailed     = "Failed"
	// needKeyNew is the need of a provider never calibrated.
	needKeyNew = "new"
	// calibrationServer is the core MCP server that calibrates, in the operator's namespace.
	calibrationServer = "accelerator-mcp"
	// calibrationTool is its tool that starts the recipe.
	calibrationTool = "calibrate_provider"
	// calibrationCallTimeout bounds one request; the tool returns as soon as the job starts.
	calibrationCallTimeout = 30 * time.Second
)

// calibrationNeed is why a provider needs calibrating; an empty Key means it does not.
type calibrationNeed struct {
	Key    string
	Reason string
}

// needOf reads the provider's need from its state and the models bound to it. A
// pending reset comes first, then a provider never calibrated, then a changed
// engine, then bound models the calibration has not measured.
func needOf(p *aiv1alpha1.ModelProvider, bound []string) calibrationNeed {
	cal := p.Status.Calibration
	if token := pendingReset(p); token != "" {
		return calibrationNeed{Key: "reset/" + token, Reason: "a calibration reset was requested"}
	}
	if neverCalibrated(cal) {
		return calibrationNeed{Key: needKeyNew, Reason: "the provider has not been calibrated"}
	}
	if engine := engineVersion(p); cal.EngineVersion != "" && engine != "" && engine != cal.EngineVersion {
		return calibrationNeed{Key: "engine/" + engine,
			Reason: fmt.Sprintf("its engine changed from %s to %s", cal.EngineVersion, engine)}
	}
	if missing := uncalibrated(cal, bound); len(missing) > 0 {
		return calibrationNeed{Key: "models/" + digest(missing),
			Reason: "models bound to it are not calibrated: " + strings.Join(missing, ", ")}
	}
	return calibrationNeed{}
}

// pendingReset is the reset token a person set that no calibration has acted on yet.
func pendingReset(p *aiv1alpha1.ModelProvider) string {
	token := p.Annotations[calibrationResetAnnotation]
	if token == "" || (p.Status.Calibration != nil && p.Status.Calibration.ResetToken == token) {
		return ""
	}
	return token
}

// neverCalibrated reports a provider with no calibration results and none completed.
func neverCalibrated(cal *aiv1alpha1.ProviderCalibration) bool {
	return cal == nil || (len(cal.Models) == 0 && cal.CompletedAt == nil)
}

func engineVersion(p *aiv1alpha1.ModelProvider) string {
	if p.Status.ProviderInfo == nil {
		return ""
	}
	return p.Status.ProviderInfo.Version
}

// uncalibrated is the bound models with no calibration result, sorted.
func uncalibrated(cal *aiv1alpha1.ProviderCalibration, bound []string) []string {
	measured := map[string]bool{}
	for _, m := range cal.Models {
		measured[m.Model] = true
	}
	var missing []string
	for _, m := range bound {
		if !measured[m] && !slices.Contains(missing, m) {
			missing = append(missing, m)
		}
	}
	slices.Sort(missing)
	return missing
}

func digest(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// shouldAsk reports whether to ask about need now. idle reports whether no crew
// call holds the provider; it is only consulted to resume a paused need.
func shouldAsk(p *aiv1alpha1.ModelProvider, need calibrationNeed, idle func() (bool, error)) (bool, error) {
	if need.Key == "" {
		return false, nil
	}
	req := p.Status.CalibrationRequest
	if req == nil || req.Key != need.Key {
		return true, nil
	}
	if recoveredSinceFailure(p, req.RequestedAt) {
		return true, nil
	}
	if !pausedSince(p.Status.Calibration, req.RequestedAt) {
		return false, nil
	}
	return idle()
}

// recoveredSinceFailure reports a calibration that Failed after the request, on a
// provider that became ready again after that: whatever failed it may have passed.
func recoveredSinceFailure(p *aiv1alpha1.ModelProvider, requested metav1.Time) bool {
	cal := p.Status.Calibration
	if cal == nil || cal.Phase != calibrationPhaseFailed || cal.UpdatedAt == nil || !cal.UpdatedAt.After(requested.Time) {
		return false
	}
	ready := meta.FindStatusCondition(p.Status.Conditions, conditionTypeReady)
	return p.Status.Ready && ready != nil && ready.LastTransitionTime.After(cal.UpdatedAt.Time)
}

// pausedSince reports whether the calibration was recorded as Waiting after the
// request was made.
func pausedSince(cal *aiv1alpha1.ProviderCalibration, requested metav1.Time) bool {
	return cal != nil && cal.Phase == calibrationPhaseWaiting &&
		cal.UpdatedAt != nil && cal.UpdatedAt.After(requested.Time)
}

// Calibrator starts calibrations.
type Calibrator interface {
	// Ready reports whether calibrations can be started now, and why not.
	Ready(ctx context.Context) (bool, string)
	// Calibrate starts the recipe on the provider and returns once it started;
	// false when a calibration of the provider was already running.
	Calibrate(ctx context.Context, provider string) (bool, error)
	// Idle reports whether no call holds a ticket on the provider.
	Idle(provider string) (bool, error)
}

// requestCalibration starts a calibration when the provider needs one and the
// calibration server is ready, and records the request on the provider's status
// with a patch of its own, so a conflict on the reconciler's status update cannot
// lose it and start the calibration twice. Failures are logged and retried on the
// next reconcile. A calibration already running is not recorded as this need's:
// the need is asked again once it ends.
func (r *ModelProviderReconciler) requestCalibration(ctx context.Context, p *aiv1alpha1.ModelProvider) {
	if r.Calibration == nil || !calibrationEnabled(p) {
		return
	}
	log := logf.FromContext(ctx).WithName("calibration")
	bound, err := r.boundModels(ctx, p)
	if err != nil {
		log.Info("could not list Models bound to the provider", "provider", p.Name, "err", err)
		return
	}
	need := needOf(p, bound)
	ask, err := shouldAsk(p, need, func() (bool, error) { return r.Calibration.Idle(p.Name) })
	if err != nil {
		log.Info("could not tell whether the provider is idle", "provider", p.Name, "err", err)
		return
	}
	if !ask {
		return
	}
	if ready, why := r.Calibration.Ready(ctx); !ready {
		log.V(1).Info("calibration waits for its server", "provider", p.Name, "why", why)
		return
	}
	started, err := r.Calibration.Calibrate(ctx, p.Name)
	if err != nil {
		log.Info("could not start a calibration", "provider", p.Name, "err", err)
		return
	}
	if !started {
		log.V(1).Info("a calibration is already running; the need waits for it to end", "provider", p.Name)
		return
	}
	log.Info("started a calibration", "provider", p.Name, "reason", need.Reason)
	req := &aiv1alpha1.CalibrationRequest{Key: need.Key, Reason: need.Reason, RequestedAt: metav1.Now()}
	r.patchStatus(ctx, p, func(s *aiv1alpha1.ModelProviderStatus) { s.CalibrationRequest = req })
	// The calibration has started: keep the request on p even if its patch failed,
	// so the reconcile's status update can still record it.
	p.Status.CalibrationRequest = req
}

func calibrationEnabled(p *aiv1alpha1.ModelProvider) bool {
	return p.Spec.Calibration == nil || p.Spec.Calibration.Enabled == nil || *p.Spec.Calibration.Enabled
}

// boundModels is the names of every enabled Model in any namespace that references
// the provider (a crew's own Model is measured too, though it is not in the catalog),
// plus the catalog's present models, as the provider's engine knows them: the Ollama tag
// for an Ollama provider, the served name (spec.model) for a vLLM one, which has no
// tag and serves what its servers are named for.
func (r *ModelProviderReconciler) boundModels(ctx context.Context, p *aiv1alpha1.ModelProvider) ([]string, error) {
	var models aiv1alpha1.ModelList
	if err := r.List(ctx, &models); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range models.Items {
		if m.Spec.ProviderRef != p.Name || m.Spec.Disabled || !m.DeletionTimestamp.IsZero() {
			continue
		}
		if name := catalogModelName(p, &m); name != "" {
			out = append(out, name)
		}
	}
	return withCatalogModels(out, p), nil
}

// withCatalogModels adds the models the provider's catalog says it has that no
// Model object named, so a model the cluster adopted by discovery is measured too.
func withCatalogModels(names []string, p *aiv1alpha1.ModelProvider) []string {
	have := make(map[string]bool, len(names))
	for _, n := range names {
		have[n] = true
	}
	for _, e := range p.Status.Models {
		if e.Present && !have[e.Name] {
			names = append(names, e.Name)
			have[e.Name] = true
		}
	}
	return names
}

// MCPCalibrator starts calibrations by calling the accelerator-mcp core server's
// calibrate_provider tool over MCP, and reads provider tickets from NATS.
type MCPCalibrator struct {
	Client    client.Reader
	Publisher *kubemootnats.Publisher
	// HTTPClient reaches the server; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// Version is the operator version it reports to the server.
	Version string
}

// errNATSUnavailable is returned when provider tickets cannot be read.
var errNATSUnavailable = errors.New("NATS is not configured")

// Ready reports whether the core server is ready with an endpoint.
func (c MCPCalibrator) Ready(ctx context.Context) (bool, string) {
	srv, err := c.server(ctx)
	if err != nil {
		return false, err.Error()
	}
	if !srv.Status.Ready || srv.Status.Endpoint == "" {
		return false, "the " + calibrationServer + " core server is not ready"
	}
	return true, ""
}

func (c MCPCalibrator) server(ctx context.Context) (*aiv1alpha1.MCPServer, error) {
	srv := &aiv1alpha1.MCPServer{}
	key := client.ObjectKey{Namespace: operatorNamespace(), Name: calibrationServer}
	if err := c.Client.Get(ctx, key, srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// Calibrate calls calibrate_provider and reports whether it started a run.
func (c MCPCalibrator) Calibrate(ctx context.Context, provider string) (bool, error) {
	srv, err := c.server(ctx)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, calibrationCallTimeout)
	defer cancel()
	transport := &mcp.SSEClientTransport{
		Endpoint:   strings.TrimSuffix(srv.Status.Endpoint, "/") + "/sse",
		HTTPClient: c.HTTPClient,
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "kubemoot-operator", Version: c.Version}, nil)
	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		return false, fmt.Errorf("connecting to %s: %w", calibrationServer, err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: calibrationTool, Arguments: map[string]any{"provider": provider},
	})
	if err != nil {
		return false, fmt.Errorf("calling %s: %w", calibrationTool, err)
	}
	if res.IsError {
		return false, fmt.Errorf("%s refused: %s", calibrationTool, toolText(res))
	}
	return startedRun(toolText(res))
}

// startedRun reads calibrate_provider's answer: {"started": true, ...} for a new
// run, {"running": true, ...} for one already running.
func startedRun(text string) (bool, error) {
	var out struct {
		Started bool `json:"started"`
		Running bool `json:"running"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || (!out.Started && !out.Running) {
		return false, fmt.Errorf("unexpected %s answer: %q", calibrationTool, text)
	}
	return out.Started, nil
}

// toolText joins the text blocks of a tool result.
func toolText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, " ")
}

// Idle reports whether no ticket on the provider is held.
func (c MCPCalibrator) Idle(provider string) (bool, error) {
	if c.Publisher == nil || !c.Publisher.Configured() {
		return false, errNATSUnavailable
	}
	keys, err := c.Publisher.ListKVKeys(bus.ProviderTicketsBucket)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if strings.HasPrefix(k, bus.ProviderTicketPrefix(provider)) {
			return false, nil
		}
	}
	return true, nil
}
