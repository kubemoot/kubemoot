package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
)

// Thread names where a signal goes: the thread and the channel of the message it answers.
type Thread struct{ ID, Channel string }

// DefaultChannel is where a signal goes when the message it answers names no channel.
const DefaultChannel = "general"

// Publisher publishes the sidecar's signals with the metadata it can fill itself. The agent's
// harness never builds a bus message.
type Publisher struct {
	Conn *nats.Conn
	// JS reaches the Object Store for spilled contributions and the heartbeat bucket. Nil turns
	// both off: contributions are then truncated inline.
	JS      nats.JetStreamContext
	ID      mesh.Identity
	Role    string
	Harness string
	// HarnessImage is the harness image, by digest when the operator knows it.
	HarnessImage string
	// Model is the model the agent is configured with, named on a signal sent before any call ran.
	Model        string
	EnabledTools []string
	Metrics      *obs.Metrics
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Send publishes a message; nil means Conn.PublishMsg. A test sets it to fail a publish.
	Send func(*nats.Msg) error

	artifacts artifactStore
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Signal publishes one signal on the thread's subject. md is merged over the base metadata; a
// large content is spilled to the artifact store. The trace context of ctx travels in the NATS
// headers and in the message body.
func (p *Publisher) Signal(ctx context.Context, to Thread, t kbus.DiscussionMessageMessageType, content string,
	md map[string]any) error {
	ctx, span := obs.Tracer().Start(ctx, "publish "+string(t))
	defer span.End()
	if to.Channel == "" {
		to.Channel = DefaultChannel
	}
	meta := p.baseMetadata(t)
	for k, v := range md {
		meta[k] = v
	}
	content = p.contentFor(ctx, to, string(t), content, meta)
	msg := kbus.DiscussionMessage{
		MessageID: uuid.NewString(), ThreadID: to.ID, AgentName: p.ID.Agent, MessageType: t, Content: content,
		Channel: to.Channel, Timestamp: p.now().UTC().Format(time.RFC3339), Metadata: meta,
	}
	msg.Traceparent, msg.Tracestate = obs.Fields(ctx)
	err := p.publish(ctx, p.ID.DiscussSubject(to.Channel, to.ID), msg)
	if err != nil {
		obs.Fail(span, err)
		return err
	}
	if p.Metrics != nil {
		p.Metrics.Signal(string(t))
	}
	return nil
}

func (p *Publisher) publish(ctx context.Context, subject string, msg kbus.DiscussionMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	m := &nats.Msg{Subject: subject, Data: data}
	obs.InjectNATS(ctx, m)
	if p.Send != nil {
		return p.Send(m)
	}
	return p.Conn.PublishMsg(m)
}

// baseMetadata is what every signal carries: its type, the role, the harness, and the model the
// agent is configured with (a signal that follows a call replaces it with the model the call ran on).
func (p *Publisher) baseMetadata(t kbus.DiscussionMessageMessageType) map[string]any {
	md := map[string]any{"signal": string(t), "role": p.Role}
	if p.Harness != "" {
		md["harness"] = p.Harness
	}
	if p.HarnessImage != "" {
		md["harnessImage"] = p.HarnessImage
	}
	if p.Model != "" {
		md["modelName"] = p.Model
	}
	if t == kbus.DiscussionMessageMessageTypeAgree {
		md["toolsUsed"] = nonNil(p.EnabledTools)
	}
	return md
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Lifecycle publishes the waking or ready signal on the lifecycle subject, which no stream holds.
func (p *Publisher) Lifecycle(ctx context.Context, signal kbus.DiscussionMessageMessageType) error {
	msg := kbus.DiscussionMessage{
		MessageID: uuid.NewString(), AgentName: p.ID.Agent, MessageType: signal, Channel: mesh.BroadcastChannel,
		Timestamp: p.now().UTC().Format(time.RFC3339), Metadata: map[string]any{"signal": string(signal)},
	}
	return p.publish(ctx, p.ID.LifecycleSubject(string(signal)), msg)
}

// Heartbeat is one liveness beat of the pod: connected to NATS, the harness answering, and when the
// last model call finished.
type Heartbeat struct {
	NATS, HarnessReady bool
	LastInference      time.Time
	Interval           time.Duration
}

// WriteHeartbeat writes the agent's liveness entry to the agent state bucket.
func (p *Publisher) WriteHeartbeat(h Heartbeat) error {
	if p.JS == nil {
		return fmt.Errorf("no JetStream")
	}
	kv, err := p.stateBucket()
	if err != nil {
		return err
	}
	beat := kbus.AgentHeartbeat{Agent: p.ID.Agent, Timestamp: p.now().UTC().Format(time.RFC3339Nano), Nats: h.NATS,
		Ollama: h.HarnessReady, Model: p.Model, IntervalSeconds: int64(h.Interval.Seconds())}
	if !h.LastInference.IsZero() {
		beat.LastInference = h.LastInference.UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(beat)
	if err != nil {
		return err
	}
	_, err = kv.Put(p.ID.HeartbeatKey(), data)
	return err
}

// stateBucket binds the agent state bucket, creating it with the 300 second expiry the contract names.
func (p *Publisher) stateBucket() (nats.KeyValue, error) {
	kv, err := p.JS.KeyValue(mesh.AgentStateBucket)
	if err == nil {
		return kv, nil
	}
	return p.JS.CreateKeyValue(&nats.KeyValueConfig{Bucket: mesh.AgentStateBucket, TTL: 300 * time.Second})
}
