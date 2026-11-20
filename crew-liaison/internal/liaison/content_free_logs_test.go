package liaison

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// A question and a secret-shaped string that must not reach the liaison's logs.
const (
	logQuestion = "why does the invoice for ACME-7731 fail"
	logSecret   = "sk-test0123456789abcdefABCDEF"
)

var (
	logMu    sync.Mutex
	logLines strings.Builder
	logOnce  sync.Once
)

func capturedLogs() string {
	logMu.Lock()
	defer logMu.Unlock()
	return logLines.String()
}

// A question is asked and its discussion stream is refused. The liaison logs the ticket and the
// crew; the question, and a refusal body that quotes it with a secret, stay out of the log.
func TestTheLiaisonsLogsCarryNoQuestionOrSecret(t *testing.T) {
	logOnce.Do(func() {
		logf.SetLogger(funcr.New(func(_, args string) {
			logMu.Lock()
			defer logMu.Unlock()
			logLines.WriteString(args + "\n")
		}, funcr.Options{Verbosity: 5}))
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/discussions/{crew}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"conversationId":"conv-42"}`)
	})
	mux.HandleFunc("GET /api/v1/discussions/{crew}/{conv}/stream", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, logQuestion+" "+logSecret, http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	crews := staticLister{crews: []Crew{{Name: testCrew, Namespace: testCrew, Ready: true}}}
	svc := NewService(crews, NewGatewayAt(func(_, crew string) string { return srv.URL + "/api/v1/discussions/" + crew }),
		NewStore(time.Hour), 1)

	_, out, _ := svc.Ask(context.Background(), nil, AskInput{Crew: testCrew, Question: logQuestion + " " + logSecret, WaitSeconds: sec(5)})
	if out.State != StateFailed {
		t.Fatalf("the refused stream should fail the ticket: %+v", out)
	}

	logged := capturedLogs()
	if !strings.Contains(logged, "discussion stream ended") {
		t.Fatalf("the liaison logged nothing of the failure, so this test proves nothing:\n%s", logged)
	}
	for _, content := range []string{logQuestion, logSecret} {
		if strings.Contains(logged, content) {
			t.Errorf("the log carries %q:\n%s", content, logged)
		}
	}
}
