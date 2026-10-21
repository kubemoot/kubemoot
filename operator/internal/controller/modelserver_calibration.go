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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// The operator calibrates a server when it needs it: it was never calibrated, a
// reset was requested, its engine changed, or Models were bound to it that the
// calibration has not measured. It asks once per need, and again for the same need
// only after the calibration paused as Waiting (crew calls were using the server,
// or the server stopped) and the server has since fallen idle, or after it Failed
// and the server has since become ready again. It asks the accelerator-mcp core server,
// whose calibrate_server tool runs the fixed recipe in the background and records
// the results on the server; no model makes a decision in it.

const (
	calibrationResetAnnotation = "kubemoot.ai/calibration-reset"
	calibrationPhaseWaiting    = "Waiting"
	calibrationPhaseFailed     = "Failed"
	// calibrationPhaseCalibrating is a run under way.
	calibrationPhaseCalibrating = "Calibrating"
	// needKeyNew is the need of a server never calibrated.
	needKeyNew = "new"
	// calibrationServer is the core MCP server that calibrates, in the operator's namespace.
	calibrationServer = "accelerator-mcp"
	// calibrationTool is its tool that starts the recipe.
	calibrationTool = "calibrate_server"
	// calibrationCallTimeout bounds one request; the tool returns as soon as the job starts.
	calibrationCallTimeout = 30 * time.Second
	// retryNote ends the reason of a request made again after a Failed run that
	// recorded no result for any model; the request is made again only once.
	retryNote = " (asked again after a failure with no results)"
	// calibrationRetry is how often a server whose calibration waits for the bus is
	// looked at again. The wait ends when the bus is ready; this only sets how soon
	// the next look happens, since nothing signals a bucket appearing.
	calibrationRetry = 30 * time.Second
)

// calibrationNeed is why a server needs calibrating; an empty Key means it does not.
type calibrationNeed struct {
	Key    string
	Reason string
}

// needOf reads the server's need from its state and the models bound to it. A
// pending reset comes first, then a server never calibrated, then a changed
// engine, then bound models the calibration has not measured.
func needOf(p *aiv1alpha1.ModelServer, bound []string) calibrationNeed {
	cal := p.Status.Calibration
	if token := pendingReset(p); token != "" {
		return calibrationNeed{Key: "reset/" + token, Reason: "a calibration reset was requested"}
	}
	if neverCalibrated(cal) {
		return calibrationNeed{Key: needKeyNew, Reason: "the server has not been calibrated"}
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
func pendingReset(p *aiv1alpha1.ModelServer) string {
	token := p.Annotations[calibrationResetAnnotation]
	if token == "" || (p.Status.Calibration != nil && p.Status.Calibration.ResetToken == token) {
		return ""
	}
	return token
}

// neverCalibrated reports a server with no calibration results and none completed.
func neverCalibrated(cal *aiv1alpha1.ServerCalibration) bool {
	return cal == nil || (!anyResolved(cal.Models) && cal.CompletedAt == nil)
}

// anyResolved reports whether any model has an answer (measured or skipped).
func anyResolved(models []aiv1alpha1.ModelCalibration) bool {
	for _, m := range models {
		if m.Resolved() {
			return true
		}
	}
	return false
}

func engineVersion(p *aiv1alpha1.ModelServer) string {
	if p.Status.ServerInfo == nil {
		return ""
	}
	return p.Status.ServerInfo.Version
}

// uncalibrated is the bound models with no calibration result, sorted.
func uncalibrated(cal *aiv1alpha1.ServerCalibration, bound []string) []string {
	measured := map[string]bool{}
	for _, m := range cal.Models {
		measured[m.Model] = m.Resolved()
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
// call holds the server; it is only consulted to resume a paused need.
func shouldAsk(p *aiv1alpha1.ModelServer, need calibrationNeed, idle func() (bool, error)) (bool, error) {
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
	if failedWithoutResults(p.Status.Calibration, req) {
		return idle()
	}
	if !pausedSince(p.Status.Calibration, req.RequestedAt) {
		return false, nil
	}
	return idle()
}

// recoveredSinceFailure reports a calibration that Failed after the request, on a
// server that became ready again after that: whatever failed it may have passed.
func recoveredSinceFailure(p *aiv1alpha1.ModelServer, requested metav1.Time) bool {
	cal := p.Status.Calibration
	if cal == nil || cal.Phase != calibrationPhaseFailed || cal.UpdatedAt == nil || !cal.UpdatedAt.After(requested.Time) {
		return false
	}
	ready := meta.FindStatusCondition(p.Status.Conditions, conditionTypeReady)
	return p.Status.Ready && ready != nil && ready.LastTransitionTime.After(cal.UpdatedAt.Time)
}

// legacyFailureSummary starts the message of a Failed run that recorded no result for
// any model: the one-line summary written when the recipe gave up after the first
// failure (a NATS error, or an embedding model it could not generate with).
const legacyFailureSummary = "no model could be measured: "

// failedWithoutResults reports a calibration that Failed with that summary and no
// per-model results. The recipe now records a result for every model it tries
// (measured, skipped, or failed with its reason), so such a record stopped before it
// learned about the models and says nothing about whether they can be measured: it is
// asked again once the server is idle. A Failed run with results stays Failed until the
// server, its models, or the reset annotation change, or the server becomes ready again
// after the failure. The retry happens once per request: the request it makes is marked
// retryNote, and a second such failure under that mark stays Failed. Times are not
// compared with the request's: a run that fails at once is recorded in the same second
// the request is stamped, and the mark alone bounds the retry.
func failedWithoutResults(cal *aiv1alpha1.ServerCalibration, req *aiv1alpha1.CalibrationRequest) bool {
	return cal != nil && cal.Phase == calibrationPhaseFailed && len(cal.Models) == 0 &&
		strings.HasPrefix(cal.Message, legacyFailureSummary) && !strings.HasSuffix(req.Reason, retryNote)
}

// pausedSince reports whether the calibration was recorded as Waiting after the
// request was made.
func pausedSince(cal *aiv1alpha1.ServerCalibration, requested metav1.Time) bool {
	return cal != nil && cal.Phase == calibrationPhaseWaiting &&
		cal.UpdatedAt != nil && cal.UpdatedAt.After(requested.Time)
}

// Calibrator starts calibrations.
type Calibrator interface {
	// Ready reports whether calibrations can be started now, and why not.
	Ready(ctx context.Context) (bool, string)
	// Calibrate starts the recipe on the server and returns once it started;
	// false when a calibration of the server was already running.
	Calibrate(ctx context.Context, server string) (bool, error)
	// Idle reports whether no call holds a ticket on the server.
	Idle(server string) (bool, error)
}

// requestCalibration starts a calibration when the server needs one and the
// calibration server and the bus are ready, and records the request on the server's
// status with a patch of its own, so a conflict on the reconciler's status update
// cannot lose it and start the calibration twice. Failures are logged and retried on
// the next reconcile. A calibration already running is not recorded as this need's:
// the need is asked again once it ends. It returns why the calibration waits for the
// bus (the cause wraps bus.ErrNotReady), or nil when it does not: a missing
// scheduler bucket or an unreachable NATS is a state to wait out, and the caller
// looks again soon. The server's Calibrated condition is derived from the wait and
// status.calibration when the reconcile stores its status (see updateStatusWaiting).
func (r *ModelServerReconciler) requestCalibration(ctx context.Context, p *aiv1alpha1.ModelServer) *busWait {
	if r.Calibration == nil {
		return nil
	}
	if !calibrationEnabled(p) {
		return nil
	}
	return r.decideCalibration(ctx, p)
}

// busWait is a calibration that waits for the bus, and why.
type busWait struct{ cause error }

// err is the cause, or nil when nothing waits.
func (w *busWait) err() error {
	if w == nil {
		return nil
	}
	return w.cause
}

// decideCalibration is requestCalibration after the checks that it has a
// calibrator and calibration is on.
func (r *ModelServerReconciler) decideCalibration(ctx context.Context, p *aiv1alpha1.ModelServer) *busWait {
	log := logf.FromContext(ctx).WithName("calibration")
	bound, err := r.boundModels(ctx, p)
	if err != nil {
		log.Info("could not list Models bound to the server", "server", p.Name, "err", err)
		return nil
	}
	need := needOf(p, downloadedOnly(p, bound))
	ask, err := shouldAsk(p, need, func() (bool, error) { return r.Calibration.Idle(p.Name) })
	if errors.Is(err, bus.ErrNotReady) {
		return &busWait{err}
	}
	if err != nil {
		log.Info("could not tell whether the server is idle", "server", p.Name, "err", err)
		return nil
	}
	if !ask {
		return nil
	}
	if gate, ok := r.Calibration.(busGate); ok {
		if err := gate.BusReady(); err != nil {
			return &busWait{err}
		}
	}
	if req := p.Status.CalibrationRequest; req != nil && req.Key == need.Key && failedWithoutResults(p.Status.Calibration, req) {
		need.Reason += retryNote
	}
	r.startCalibration(ctx, p, need)
	return nil
}

// startCalibration asks the calibration server to calibrate and records the request.
func (r *ModelServerReconciler) startCalibration(ctx context.Context, p *aiv1alpha1.ModelServer, need calibrationNeed) {
	log := logf.FromContext(ctx).WithName("calibration")
	if ready, why := r.Calibration.Ready(ctx); !ready {
		log.V(1).Info("calibration waits for its server", "server", p.Name, "why", why)
		return
	}
	started, err := r.Calibration.Calibrate(ctx, p.Name)
	if err != nil {
		log.Info("could not start a calibration", "server", p.Name, "err", err)
		return
	}
	if !started {
		log.V(1).Info("a calibration is already running; the need waits for it to end", "server", p.Name)
		return
	}
	log.Info("started a calibration", "server", p.Name, "reason", need.Reason)
	req := &aiv1alpha1.CalibrationRequest{Key: need.Key, Reason: need.Reason, RequestedAt: metav1.Now()}
	r.patchStatus(ctx, p, func(s *aiv1alpha1.ModelServerStatus) { s.CalibrationRequest = req })
	// The calibration has started: keep the request on p even if its patch failed,
	// so the reconcile's status update can still record it.
	p.Status.CalibrationRequest = req
}

// busGate is a Calibrator that can tell whether the bus the calibration reads and
// writes (NATS and the scheduler's buckets) is ready.
type busGate interface {
	// BusReady is nil when it is, else an error wrapping bus.ErrNotReady.
	BusReady() error
}

// retryWhileWaiting shortens the requeue of a server whose calibration waits for the bus.
func retryWhileWaiting(result ctrl.Result, waiting *busWait) ctrl.Result {
	if waiting != nil && (result.RequeueAfter == 0 || result.RequeueAfter > calibrationRetry) {
		result.RequeueAfter = calibrationRetry
	}
	return result
}

func calibrationEnabled(p *aiv1alpha1.ModelServer) bool {
	return p.Spec.Calibration == nil || p.Spec.Calibration.Enabled == nil || *p.Spec.Calibration.Enabled
}

// boundModels is the names of every enabled Model in any namespace that references
// the server (a crew's own Model is measured too, though it is not in the catalog),
// plus the catalog's present models, as the server's engine knows them: the Ollama tag
// for an Ollama server, the served name (spec.model) for a vLLM one, which has no
// tag and serves what its servers are named for.
func (r *ModelServerReconciler) boundModels(ctx context.Context, p *aiv1alpha1.ModelServer) ([]string, error) {
	var models aiv1alpha1.ModelList
	if err := r.List(ctx, &models); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range models.Items {
		if m.Spec.ServerRef != p.Name || m.Spec.Disabled || !m.DeletionTimestamp.IsZero() {
			continue
		}
		if name := catalogModelName(p, &m); name != "" {
			out = append(out, name)
		}
	}
	return withCatalogModels(out, p), nil
}

// withCatalogModels adds the models the server's catalog says it has that no
// Model object named, so a model the cluster adopted by discovery is measured too.
func withCatalogModels(names []string, p *aiv1alpha1.ModelServer) []string {
	have := make(map[string]bool, len(names))
	for _, n := range names {
		have[n] = true
	}
	for _, name := range p.PresentCatalogModels() {
		if !have[name] {
			names = append(names, name)
			have[name] = true
		}
	}
	return names
}

// downloadedOnly keeps the models the server has on disk, since the calibration
// server measures only those. A server that has reported no models leaves the list
// as it is.
func downloadedOnly(p *aiv1alpha1.ModelServer, names []string) []string {
	if p.Status.Capacity == nil || len(p.Status.Capacity.AvailableModels) == 0 {
		return names
	}
	var out []string
	for _, n := range names {
		if p.IsDownloaded(n) {
			out = append(out, n)
		}
	}
	return out
}

// MCPCalibrator starts calibrations by calling the accelerator-mcp core server's
// calibrate_server tool over MCP, and reads server tickets from NATS.
type MCPCalibrator struct {
	Client    client.Reader
	Publisher *kubemootnats.Publisher
	// HTTPClient reaches the server; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// Version is the operator version it reports to the server.
	Version string
}

// errNATSUnavailable is returned when server tickets cannot be read.
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

// Calibrate calls calibrate_server and reports whether it started a run.
func (c MCPCalibrator) Calibrate(ctx context.Context, server string) (bool, error) {
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
		Name: calibrationTool, Arguments: map[string]any{"server": server},
	})
	if err != nil {
		return false, fmt.Errorf("calling %s: %w", calibrationTool, err)
	}
	if res.IsError {
		return false, fmt.Errorf("%s refused: %s", calibrationTool, toolText(res))
	}
	return startedRun(toolText(res))
}

// startedRun reads calibrate_server's answer: {"started": true, ...} for a new
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

// BusReady is nil when NATS is reachable and both scheduler buckets the calibration
// reads and writes exist (tickets, which every probe claims, and the performance
// history, which a reset clears and probes record into), else an error wrapping
// bus.ErrNotReady. A missing bucket is not read as "no tickets": the calibration
// itself writes tickets, so it cannot run without the bucket. Without a configured
// NATS there is nothing to wait for.
func (c MCPCalibrator) BusReady() error {
	if c.Publisher == nil || !c.Publisher.Configured() {
		return nil
	}
	return c.Publisher.BucketsReady(bus.ServerTicketsBucket, bus.PerformanceBucket)
}

// Idle reports whether no ticket on the server is held. It returns an error that
// wraps bus.ErrNotReady while the bus is not ready, never "idle".
func (c MCPCalibrator) Idle(server string) (bool, error) {
	if c.Publisher == nil || !c.Publisher.Configured() {
		return false, errNATSUnavailable
	}
	if err := c.BusReady(); err != nil {
		return false, err
	}
	keys, err := c.Publisher.ListKVKeys(bus.ServerTicketsBucket)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if strings.HasPrefix(k, bus.ServerTicketPrefix(server)) {
			return false, nil
		}
	}
	return true, nil
}
