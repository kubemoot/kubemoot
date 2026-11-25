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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// The lifecycle server answers agents that ask, over NATS request and reply, to make
// a model ready on a server or to release it. The engine adapter is the one place
// that wakes and sleeps a vLLM server, so the agent runtime carries no second
// implementation. A reply is sent when the server reports the model in the state
// asked for, not when the request was accepted.

const (
	// lifecyclePollInterval is how often the server is asked for the model's state
	// while the adapter's call has returned and the state has not yet followed.
	lifecyclePollInterval = time.Second
	// lifecycleSafetyLimit ends a request in which the server never reaches the
	// state asked for. It is a safety net: the normal path ends on the reported state.
	lifecycleSafetyLimit = 30 * time.Minute
)

// switchRefresher publishes the server state after a model changed state, so agents
// see the new state without waiting for the next probe.
type switchRefresher interface {
	RefreshAfterSwitch(ctx context.Context, server *aiv1alpha1.ModelServer, model string, switchMs int64)
	// SetDraining marks models as draining, or not, and publishes the state. It returns
	// the failure to publish.
	SetDraining(ctx context.Context, server *aiv1alpha1.ModelServer, models []string, draining bool) error
	// PlacedCalls is the ids of the placement tickets held for the models.
	PlacedCalls(ctx context.Context, server *aiv1alpha1.ModelServer, models []string) ([]string, error)
}

// subscriber is the slice of the NATS publisher the server uses.
type subscriber interface {
	Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error)
}

// ModelLifecycleServer serves ModelLifecycleRequests on kubemoot.server.model.*.
type ModelLifecycleServer struct {
	Reader     client.Reader
	Subscriber subscriber
	// Backoff paces subscribe retries while NATS is unreachable; zero is kubemootnats.DefaultBackoff.
	Backoff   kubemootnats.Backoff
	Refresher switchRefresher
	// Engines picks the adapter by server type; nil uses DefaultEngines.
	Engines *engine.Registry
	// HTTPClient reaches the model servers; nil uses a client with no timeout of its
	// own, because a wake is waited on by state, not by a clock.
	HTTPClient *http.Client
	// PollInterval and SafetyLimit default to lifecyclePollInterval and lifecycleSafetyLimit.
	PollInterval time.Duration
	SafetyLimit  time.Duration

	// locks serializes the requests of one server (a channel of one turn per server
	// name, so a waiter can give up with its context): waking a model first puts the
	// awake one to sleep, so two requests on one accelerator must not interleave.
	locks sync.Map

	// waiting counts the ready requests waiting for a server's turn, by server and the ticket
	// of the call they are made for.
	waitMu  sync.Mutex
	waiting map[string]int
}

// NeedLeaderElection keeps one operator answering.
func (s *ModelLifecycleServer) NeedLeaderElection() bool { return true }

// Start subscribes and answers until ctx ends.
func (s *ModelLifecycleServer) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("model-lifecycle")
	if s.Subscriber == nil {
		<-ctx.Done()
		return nil
	}
	subject := bus.ModelLifecycleSubjectPrefix + "*"
	sub, err := kubemootnats.SubscribeUntilReady(ctx, func() (*nats.Subscription, error) {
		return s.Subscriber.Subscribe(subject, func(m *nats.Msg) {
			// A wake can take minutes; the subscription's callbacks run one at a time.
			go s.respond(ctx, m)
		})
	}, s.backoff(), func(err error, retryIn time.Duration) {
		log.Info("model lifecycle server not subscribed yet; agents cannot wake models", "err", err.Error(), "retryIn", retryIn.String())
	})
	if err != nil || sub == nil {
		log.Info("model lifecycle server idle: NATS is not configured or the operator is stopping")
		<-ctx.Done()
		return nil
	}
	log.Info("model lifecycle server subscribed", "subject", bus.ModelLifecycleSubjectPrefix+"*")
	<-ctx.Done()
	_ = sub.Drain()
	return nil
}

// backoff is the configured retry pacing, or the default.
func (s *ModelLifecycleServer) backoff() kubemootnats.Backoff {
	if s.Backoff.First <= 0 {
		return kubemootnats.DefaultBackoff
	}
	return s.Backoff
}

// respond answers one request message.
func (s *ModelLifecycleServer) respond(ctx context.Context, m *nats.Msg) {
	reply := s.Handle(ctx, m.Subject, m.Data)
	body, err := json.Marshal(reply)
	if err != nil {
		return
	}
	if err := m.Respond(body); err != nil {
		logf.FromContext(ctx).WithName("model-lifecycle").V(1).Info("could not reply", "subject", m.Subject, "err", err)
	}
}

// Handle carries out one request and returns the reply to send.
func (s *ModelLifecycleServer) Handle(ctx context.Context, subject string, data []byte) bus.ModelLifecycleReply {
	var req bus.ModelLifecycleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return failed("the request is not a ModelLifecycleRequest: " + err.Error())
	}
	if req.Model == "" || (req.Action != bus.ModelLifecycleRequestActionReady && req.Action != bus.ModelLifecycleRequestActionRelease) {
		return failed("the request needs a model and an action of ready or release")
	}
	server, err := s.serverFor(ctx, strings.TrimPrefix(subject, bus.ModelLifecycleSubjectPrefix))
	if err != nil {
		return failed(err.Error())
	}
	if server.Spec.Type != aiv1alpha1.ServerTypeVLLM {
		return failed(fmt.Sprintf("server %s is %q: only engines that put models to sleep take lifecycle requests", server.Name, server.Spec.Type))
	}
	// The safety limit covers the wait for the server's lock as well as the work, so a
	// request queued behind a stuck wake ends before its asker gives up.
	ctx, cancel := context.WithTimeout(ctx, s.safetyLimit())
	defer cancel()
	if req.Action == bus.ModelLifecycleRequestActionReady {
		s.noteWaiting(server.Name, req.TicketID, 1)
	}
	release, err := s.lockServer(ctx, server.Name)
	if req.Action == bus.ModelLifecycleRequestActionReady {
		s.noteWaiting(server.Name, req.TicketID, -1)
	}
	if err != nil {
		return failed(err.Error())
	}
	defer release()
	return s.carryOut(ctx, server, req)
}

func (s *ModelLifecycleServer) safetyLimit() time.Duration {
	if s.SafetyLimit > 0 {
		return s.SafetyLimit
	}
	return lifecycleSafetyLimit
}

// lockServer waits for the server's turn, or for ctx. It returns the function that
// gives the turn up.
func (s *ModelLifecycleServer) lockServer(ctx context.Context, name string) (func(), error) {
	slot, _ := s.locks.LoadOrStore(name, make(chan struct{}, 1))
	turn := slot.(chan struct{})
	select {
	case turn <- struct{}{}:
		return func() { <-turn }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("another request on server %s did not finish before the safety limit", name)
	}
}

func failed(msg string) bus.ModelLifecycleReply {
	return bus.ModelLifecycleReply{Ok: false, Error: msg}
}

// serverFor finds the ModelServer whose escaped name is token.
func (s *ModelLifecycleServer) serverFor(ctx context.Context, token string) (*aiv1alpha1.ModelServer, error) {
	list := &aiv1alpha1.ModelServerList{}
	if err := s.Reader.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing ModelServers: %w", err)
	}
	for i := range list.Items {
		if bus.KeyToken(list.Items[i].Name) == token {
			return &list.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no ModelServer matches the subject token %q", token)
}

// carryOut makes the model ready or releases it through the engine adapter, then
// waits for the server to report the state, and publishes the new state.
func (s *ModelLifecycleServer) carryOut(ctx context.Context, server *aiv1alpha1.ModelServer, req bus.ModelLifecycleRequest) bus.ModelLifecycleReply {
	want := engine.StateAwake
	if req.Action == bus.ModelLifecycleRequestActionRelease {
		want = engine.StateAsleep
	}
	adapter, err := s.adapterFor(server.Spec.Type)
	if err != nil {
		return failed(err.Error())
	}
	spec := server.EffectiveSpec()
	ctx = s.withDrainPublishing(ctx, server)

	state, err := stateOf(ctx, adapter, spec, req.Model)
	if err != nil {
		return failed(err.Error())
	}
	if state == want {
		return bus.ModelLifecycleReply{Ok: true, State: bus.ModelLifecycleReplyState(want)}
	}
	started := time.Now()
	if err := act(ctx, adapter, spec, req); err != nil {
		return failed(err.Error())
	}
	if err := s.waitForState(ctx, adapter, spec, req.Model, want); err != nil {
		return failed(err.Error())
	}
	var switchMs int64
	if want == engine.StateAwake {
		switchMs = time.Since(started).Milliseconds()
	}
	if s.Refresher != nil {
		s.Refresher.RefreshAfterSwitch(ctx, server, req.Model, switchMs)
	}
	return bus.ModelLifecycleReply{Ok: true, State: bus.ModelLifecycleReplyState(want), SwitchMs: switchMs}
}

// withDrainPublishing gives the adapter what a switch needs: to publish a model as draining
// before it waits for the model's calls to finish, and to count the calls placed on it.
// A call waiting on this server's turn for the switch to finish is placed on the draining
// model too; it is not outstanding work, so it is not counted.
func (s *ModelLifecycleServer) withDrainPublishing(ctx context.Context, server *aiv1alpha1.ModelServer) context.Context {
	if s.Refresher == nil {
		return ctx
	}
	return engine.WithDrain(ctx, engine.Drain{
		Notify: func(c context.Context, models []string, draining bool) error {
			return s.Refresher.SetDraining(c, server, models, draining)
		},
		Placed: func(c context.Context, models []string) (int, error) {
			ids, err := s.Refresher.PlacedCalls(c, server, models)
			if err != nil {
				return 0, err
			}
			return s.outstanding(server.Name, ids), nil
		},
	})
}

// waiterKey is the key of a request waiting for the server's turn on behalf of a ticket.
func waiterKey(server, ticket string) string { return server + "\x00" + ticket }

// noteWaiting counts a ready request made for the ticket as waiting for the server's turn,
// or no longer waiting. A request that names no ticket is not tracked: it is not known to
// stand for any placed call.
func (s *ModelLifecycleServer) noteWaiting(server, ticket string, delta int) {
	if ticket == "" {
		return
	}
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.waiting == nil {
		s.waiting = map[string]int{}
	}
	key := waiterKey(server, ticket)
	s.waiting[key] += delta
	if s.waiting[key] <= 0 {
		delete(s.waiting, key)
	}
}

// outstanding is how many of the tickets are calls that must finish before the switch:
// those whose ready request is not waiting for the server's turn. A ticket whose call
// waits for this switch cannot finish before it, so it is not outstanding work.
func (s *ModelLifecycleServer) outstanding(server string, tickets []string) int {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	n := 0
	for _, id := range tickets {
		if s.waiting[waiterKey(server, id)] <= 0 {
			n++
		}
	}
	return n
}

// adapterFor is the engine adapter for a server type, over a client with no timeout of
// its own unless the server was given one: a wake is waited on by state, not by a clock.
func (s *ModelLifecycleServer) adapterFor(t aiv1alpha1.ServerType) (engine.Adapter, error) {
	httpClient := s.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	engines := s.Engines
	if engines == nil {
		engines = DefaultEngines()
	}
	return engines.Adapter(t, httpClient)
}

// act asks the adapter to do what the request says.
func act(ctx context.Context, adapter engine.Adapter, spec aiv1alpha1.ModelServerSpec, req bus.ModelLifecycleRequest) error {
	if req.Action == bus.ModelLifecycleRequestActionRelease {
		return adapter.Release(ctx, spec, req.Model)
	}
	return adapter.Ready(ctx, spec, req.Model)
}

// stateOf is the model's state the server reports now.
func stateOf(ctx context.Context, adapter engine.Adapter, spec aiv1alpha1.ModelServerSpec, model string) (engine.ModelState, error) {
	snap, err := adapter.Probe(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("probing the server: %w", err)
	}
	st, ok := snap.ModelStates[model]
	if !ok {
		return "", fmt.Errorf("the server does not report model %q", model)
	}
	return st, nil
}

// waitForState polls until the server reports the state, even though the adapter's call
// returned after the server answered: a server may report the old state for a moment
// after it accepted the change, and the reply is promised on the reported state. The
// interval paces the reads; it never ends the wait.
// It polls until the server reports want for the model. It ends on that
// state, on a probe that cannot name the model, or on ctx (the safety limit).
func (s *ModelLifecycleServer) waitForState(ctx context.Context, adapter engine.Adapter, spec aiv1alpha1.ModelServerSpec, model string, want engine.ModelState) error {
	every := s.PollInterval
	if every <= 0 {
		every = lifecyclePollInterval
	}
	for {
		state, err := stateOf(ctx, adapter, spec, model)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return err
		}
		if err == nil && state == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the server did not report %q as %s before the safety limit", model, want)
		case <-time.After(every):
		}
	}
}

// switchLog is the observed time to make each model ready, by server and model.
type switchLog struct {
	mu   sync.Mutex
	byOf map[string]map[string]int64
}

// Record keeps the latest observed switch time for the model; a zero is not a switch.
func (l *switchLog) Record(server, model string, ms int64) {
	if ms <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byOf == nil {
		l.byOf = map[string]map[string]int64{}
	}
	if l.byOf[server] == nil {
		l.byOf[server] = map[string]int64{}
	}
	l.byOf[server][model] = ms
}

// For is a copy of the server's observed switch times; nil when there are none.
func (l *switchLog) For(server string) map[string]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byOf[server]) == 0 {
		return nil
	}
	out := make(map[string]int64, len(l.byOf[server]))
	for m, ms := range l.byOf[server] {
		out[m] = ms
	}
	return out
}

// Forget drops what was observed for a server that no longer exists.
func (l *switchLog) Forget(server string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byOf, server)
}

// RefreshAfterSwitch records how long the model took to become ready and publishes the
// server state read now, so agents see the model awake without waiting for the next
// probe. A failed probe leaves the last published state as it was.
func (r *ModelServerReconciler) RefreshAfterSwitch(ctx context.Context, server *aiv1alpha1.ModelServer, model string, switchMs int64) {
	r.switches.Record(server.Name, model, switchMs)
	if _, err := r.probeSnapshot(ctx, server, r.refreshClient()); err != nil {
		logf.FromContext(ctx).V(1).Info("could not refresh the server state after a switch", "server", server.Name, "err", err)
		return
	}
	r.publishStateToKV(ctx, server)
}

// refreshClient is the client a state refresh probes with.
func (r *ModelServerReconciler) refreshClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// mergeSwitchCosts adds the observed switch times to what the engine reported.
func mergeSwitchCosts(reported, observed map[string]int64) map[string]int64 {
	if len(observed) == 0 {
		return reported
	}
	out := make(map[string]int64, len(reported)+len(observed))
	for m, ms := range reported {
		out[m] = ms
	}
	for m, ms := range observed {
		out[m] = ms
	}
	return out
}

var _ switchRefresher = (*ModelServerReconciler)(nil)
var _ subscriber = (*kubemootnats.Publisher)(nil)
