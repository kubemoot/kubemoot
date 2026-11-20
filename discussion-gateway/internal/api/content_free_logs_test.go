package api

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// A question and a secret-shaped string that must not reach the gateway's logs.
const (
	logQuestion = "why does the invoice for ACME-7731 fail"
	logSecret   = "sk-test0123456789abcdefABCDEF"
)

var (
	logMu    sync.Mutex
	logLines strings.Builder
	logOnce  sync.Once
)

// captureLogs sends everything the gateway logs, at any verbosity, to a buffer the test reads.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	logOnce.Do(func() {
		logf.SetLogger(funcr.New(func(_, args string) {
			logMu.Lock()
			defer logMu.Unlock()
			logLines.WriteString(args + "\n")
		}, funcr.Options{Verbosity: 5}))
	})
	return func() string {
		logMu.Lock()
		defer logMu.Unlock()
		return logLines.String()
	}
}

// A question is posted, its thread restarts under a new id, and the stream follows it to the
// answer. The question and the answer carry the secret; the gateway logs ids and outcomes.
func TestTheGatewaysLogsCarryNoQuestionAnswerOrSecret(t *testing.T) {
	logs := captureLogs(t)
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	since := time.Now().UTC().Format(time.RFC3339Nano)

	resp, err := http.Post(srv.URL+"/api/v1/discussions/pilot", "application/json",
		strings.NewReader(`{"message":"`+logQuestion+` `+logSecret+`","conversationId":"conv-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the question was not queued: %d", resp.StatusCode)
	}

	publishDiscuss(t, js, turnStart("A", "conv-1", logQuestion+" "+logSecret))
	publishDiscuss(t, js, msgOf("A", "a1", "failure", "the tool said "+logSecret))
	publishDiscuss(t, js, turnStart("B", "conv-1", logQuestion+" "+logSecret))
	publishDiscuss(t, js, msgOf("B", "b1", msgSynthesis, "The answer to "+logQuestion+" is "+logSecret))
	publishDiscuss(t, js, msgOf("B", "b2", "thread_close", ""))
	readStream(t, srv, "/api/v1/discussions/pilot/conv-1/stream?since="+url.QueryEscape(since), nil)
	assertGatewayLogHasNoContent(t, logs())
}

func assertGatewayLogHasNoContent(t *testing.T, logged string) {
	t.Helper()
	if !strings.Contains(logged, "Queued discussion request") {
		t.Fatalf("the gateway logged nothing of the request, so this test proves nothing:\n%s", logged)
	}
	for _, content := range []string{logQuestion, logSecret} {
		if strings.Contains(logged, content) {
			t.Errorf("the log carries %q:\n%s", content, logged)
		}
	}
}
