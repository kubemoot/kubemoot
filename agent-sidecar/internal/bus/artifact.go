package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
)

// The limits agent-runtime uses, so a contribution is spilled at the same size.
const (
	spillThreshold = 4096
	inlineLimit    = 5000
	previewChars   = 800
	artifactTTL    = 48 * time.Hour
	markerPrefix   = "[ARTIFACT key="
)

// artifactStore binds the Object Store bucket once.
type artifactStore struct {
	once  sync.Once
	store nats.ObjectStore
	err   error
}

func (a *artifactStore) bind(js nats.JetStreamContext) (nats.ObjectStore, error) {
	a.once.Do(func() {
		a.store, a.err = js.ObjectStore(mesh.ArtifactBucket)
		if errors.Is(a.err, nats.ErrStreamNotFound) || errors.Is(a.err, nats.ErrBucketNotFound) {
			a.store, a.err = js.CreateObjectStore(&nats.ObjectStoreConfig{Bucket: mesh.ArtifactBucket,
				TTL: artifactTTL, Storage: nats.FileStorage})
		}
	})
	return a.store, a.err
}

// contentFor decides the inline content of a signal. Small content goes inline. Content over the
// spill threshold is written whole to the Object Store, announced to the materializer, described
// in md.artifact, and replaced inline by a marker that names the artifact and carries no data. If the
// spill fails, the content is truncated inline and the signal still goes out.
func (p *Publisher) contentFor(ctx context.Context, to Thread, signal, content string, md map[string]any) string {
	if len(content) <= spillThreshold {
		return truncate(content, inlineLimit)
	}
	ref, err := p.spill(ctx, to, signal, content)
	if err != nil {
		md["lastError"] = truncate("artifact spill failed, content truncated inline: "+err.Error(), 500)
		return truncate(content, inlineLimit)
	}
	md["artifact"] = ref
	return markerPrefix + ref.Key + " bytes=" + strconv.FormatInt(ref.Bytes, 10) +
		" - the FULL data is in the file /artifacts/" + ref.Key +
		"; READ it (execute_code, or head/tail/grep/count over the artifact key) - do NOT answer from this " +
		"reference line, it carries no data]"
}

func (p *Publisher) spill(ctx context.Context, to Thread, signal, content string) (kbus.ArtifactReference, error) {
	var ref kbus.ArtifactReference
	if p.JS == nil {
		return ref, errors.New("no JetStream")
	}
	store, err := p.artifacts.bind(p.JS)
	if err != nil {
		return ref, err
	}
	data := []byte(content)
	key := p.ID.ArtifactKey(to.ID, signal, uuid.NewString())
	if _, err := store.PutBytes(key, data); err != nil {
		return ref, err
	}
	back, err := store.GetBytes(key)
	if err != nil || !bytes.Equal(back, data) {
		_ = store.Delete(key)
		return ref, errors.Join(errors.New("artifact read-back differs from what was written"), err)
	}
	ref = kbus.ArtifactReference{Bucket: mesh.ArtifactBucket, Key: key, Bytes: int64(len(data)), Agent: p.ID.Agent,
		Tool: signal, CreatedAt: p.now().UTC().Format(time.RFC3339), Preview: truncate(content, previewChars)}
	notice, err := json.Marshal(kbus.ArtifactNotice{Artifact: ref})
	if err != nil {
		return ref, err
	}
	m := &nats.Msg{Subject: p.ID.ArtifactSubject(to.ID), Data: notice}
	obs.InjectNATS(ctx, m)
	return ref, p.Conn.PublishMsg(m)
}

// truncate cuts s to at most n characters (bytes, on a rune boundary).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
