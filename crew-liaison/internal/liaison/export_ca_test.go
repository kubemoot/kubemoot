package liaison

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func exporterService(t *testing.T, url, caFile string) *Service {
	t.Helper()
	exporter, err := NewExporterWithCA(url, caFile)
	if err != nil {
		t.Fatal(err)
	}
	crews := staticLister{crews: []Crew{{Name: testCrew, Namespace: "ns", Ready: true}}}
	return NewService(crews, NewGateway(), NewStore(time.Hour), 1).WithExporter(exporter)
}

func TestTheExportOverTLSPresentsTheTokenOnlyToAServerItVerifies(t *testing.T) {
	var auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(zipOf(oneReport)))
	}))
	defer srv.Close()
	in := ExportConversationInput{Crew: testCrew, ConversationIDs: []string{convID}}

	_, out, err := exporterService(t, srv.URL, writeCA(t, srv)).ExportConversation(withCredential(context.Background(), "alice-token"), nil, in)
	if err != nil || !out.Delivered || auth != "Bearer alice-token" {
		t.Fatalf("trusted server: %v %+v %q", err, out, auth)
	}

	auth = ""
	res, _, _ := exporterService(t, srv.URL, unrelatedCA(t)).ExportConversation(withCredential(context.Background(), "alice-token"), nil, in)
	if res == nil || !res.IsError {
		t.Fatalf("an untrusted certificate was accepted: %+v", res)
	}
	if auth != "" {
		t.Fatal("the token reached a server whose certificate is not trusted")
	}
}

func TestNewExporterWithCARefusesAnUnusableCAFile(t *testing.T) {
	if _, err := NewExporterWithCA("https://op-report.ns.svc:8082", "/nonexistent/ca.crt"); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
	if !strings.Contains(NewExporter("https://x/").baseURL, "https://x") {
		t.Fatal("base URL")
	}
}
