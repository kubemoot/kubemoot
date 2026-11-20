package bus_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// leakedSecret is a credential-shaped string the harness puts in its answer and its failure.
// askNodes, the question of the turn, is the other text the sidecar's logs must not carry.
const leakedSecret = "sk-test0123456789abcdefABCDEF"

// quietLogger logs every level to buf, so a line at any level shows up.
func quietLogger(buf *logBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// A turn whose signals cannot be published is the sidecar's noisiest path: it logs every failed
// publish with its error. The answer and the failure carry the question and the secret, and the
// logs must not.
func TestTheSidecarsLogsOfAnUnpublishableTurnCarryNoQuestionOrSecret(t *testing.T) {
	var logs logBuf
	var p *pod
	p = newPod(t, options{log: quietLogger(&logs)}, func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
		p.nc.Close()
		return a2a.Result{}, errors.New("upstream said " + askNodes + " with token " + leakedSecret)
	})
	p.advisory(t, "t-failed", agent)
	deadline := time.Now().Add(waitFor)
	for logs.count("level=") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if logs.count("level=") == 0 {
		t.Fatal("the runner logged nothing, so this test proves nothing")
	}
	assertNoContent(t, &logs)
}

func assertNoContent(t *testing.T, logs *logBuf) {
	t.Helper()
	for _, content := range []string{askNodes, leakedSecret} {
		if n := logs.count(content); n != 0 {
			t.Errorf("the log carries %q %d times", content, n)
		}
	}
}
