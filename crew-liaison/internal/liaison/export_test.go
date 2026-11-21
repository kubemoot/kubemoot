package liaison

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeOperator imitates the operator's conversation-export endpoint.
type fakeOperator struct {
	mu      sync.Mutex
	calls   []string // "report" or "zip", in order
	tokens  []string
	queries []string
	status  int
	report  string
	zip     string
	srv     *httptest.Server
}

func newFakeOperator(report, zipBody string) *fakeOperator {
	f := &fakeOperator{status: http.StatusOK, report: report, zip: zipBody}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.queries = append(f.queries, r.URL.Path+"?"+r.URL.RawQuery)
		if f.status != http.StatusOK {
			http.Error(w, "refused by the operator", f.status)
			return
		}
		if r.URL.Query().Get("report") == "true" {
			f.calls = append(f.calls, "report")
			_, _ = w.Write([]byte(f.report))
			return
		}
		f.calls = append(f.calls, "zip")
		_, _ = w.Write([]byte(f.zip))
	}))
	return f
}

func exportService(t *testing.T, f *fakeOperator) *Service {
	t.Helper()
	crews := staticLister{crews: []Crew{{Name: testCrew, Namespace: "ns", Ready: true}}}
	exporter := NewExporterWith(f.srv.URL, f.srv.Client(), func() (string, error) { return "sa-token", nil })
	return NewService(crews, NewGateway(), NewStore(time.Hour), 1).WithExporter(exporter)
}

// zipOf is an export zip whose manifest.json is manifest, plus a conversation file.
func zipOf(manifest string) string {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{"conversation.md", "# c"}, {"manifest.json", manifest}} {
		w, _ := zw.Create(f[0])
		_, _ = w.Write([]byte(f[1]))
	}
	_ = zw.Close()
	return buf.String()
}

const (
	convID = "conv-1"
	zoneNY = "America/New_York"
)

const oneReport = `{"kind":"kubemoot.conversation-export","conversationId":"conv-1","turns":2}`

func TestExportReturnsTheZipAsAnEmbeddedResourceWithItsManifest(t *testing.T) {
	f := newFakeOperator(oneReport, zipOf(oneReport))
	defer f.srv.Close()
	res, out, err := exportService(t, f).ExportConversation(context.Background(), nil,
		ExportConversationInput{Crew: testCrew, ConversationIDs: []string{" conv-1 "}, TimeZone: zoneNY})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	resource, ok := res.Content[0].(*mcp.EmbeddedResource)
	if !ok || string(resource.Resource.Blob) != zipOf(oneReport) || resource.Resource.MIMEType != "application/zip" {
		t.Fatalf("content = %+v, want the zip as an embedded resource", res.Content[0])
	}
	assertDelivered(t, out)
	assertOperatorCalls(t, f)
}

func assertDelivered(t *testing.T, out ExportConversationOutput) {
	t.Helper()
	if !out.Delivered || out.Filename != "hello-conversation-conv-1.zip" || out.SizeBytes != len(zipOf(oneReport)) {
		t.Errorf("output = %+v", out)
	}
	if out.Manifest["conversationId"] != convID {
		t.Errorf("manifest = %+v, want the one read from the zip", out.Manifest)
	}
}

func assertOperatorCalls(t *testing.T, f *fakeOperator) {
	t.Helper()
	if strings.Join(f.calls, ",") != "zip" {
		t.Errorf("calls = %v, want one zip request: the manifest is read from the zip", f.calls)
	}
	for _, tok := range f.tokens {
		if tok != "Bearer sa-token" {
			t.Errorf("Authorization = %q, want the liaison's service account token", tok)
		}
	}
	if q := f.queries[0]; !strings.HasPrefix(q, "/conversation-export/ns/hello?") || !strings.Contains(q, "id=conv-1") || !strings.Contains(q, "timeZone=America%2FNew_York") {
		t.Errorf("query = %q", q)
	}
}

func TestExportOfSeveralNamesEachIDAndReturnsTheBundle(t *testing.T) {
	bundle := `{"included":[{"conversationId":"a"}],"refused":[{"conversationId":"b"}]}`
	f := newFakeOperator(bundle, zipOf(bundle))
	defer f.srv.Close()
	res, out, _ := exportService(t, f).ExportConversation(context.Background(), nil,
		ExportConversationInput{Crew: testCrew, ConversationIDs: []string{"a", "b"}})
	if res == nil || !out.Delivered || out.Filename != "hello-conversations.zip" {
		t.Fatalf("output = %+v", out)
	}
	if q := f.queries[0]; !strings.Contains(q, "id=a") || !strings.Contains(q, "id=b") {
		t.Errorf("query = %q", q)
	}
}

func TestExportOfSeveralThatAreAllRefusedReturnsOnlyTheManifest(t *testing.T) {
	refused := `{"included":[],"refused":[{"conversationId":"a"},{"conversationId":"b"}]}`
	f := newFakeOperator(refused, zipOf(refused))
	defer f.srv.Close()
	res, out, _ := exportService(t, f).ExportConversation(context.Background(), nil,
		ExportConversationInput{Crew: testCrew, ConversationIDs: []string{"a", "b"}})
	if res != nil || out.Delivered || out.Note == "" || strings.Join(f.calls, ",") != "zip" {
		t.Errorf("res = %v, output = %+v, calls = %v", res, out, f.calls)
	}
}

func TestAZipTooLargeForAToolResultIsNotReturnedAndTheManifestSaysSo(t *testing.T) {
	f := newFakeOperator(oneReport, strings.Repeat("z", maxInlineExport+1))
	defer f.srv.Close()
	res, out, _ := exportService(t, f).ExportConversation(context.Background(), nil,
		ExportConversationInput{Crew: testCrew, ConversationIDs: []string{convID}})
	if res != nil || out.Delivered || !strings.Contains(out.Note, "kmctl conversation export") || out.Manifest["turns"] == nil ||
		strings.Join(f.calls, ",") != "zip,report" {
		t.Errorf("res = %v, output = %+v", res, out)
	}
}

func TestExportAnswersTheOperatorsRefusalAsAToolError(t *testing.T) {
	cases := map[int]string{
		http.StatusNotFound:            "expired",
		http.StatusForbidden:           "refused by the operator",
		http.StatusBadRequest:          "refused by the operator",
		http.StatusInternalServerError: "could not build the export",
	}
	for status, want := range cases {
		f := newFakeOperator(oneReport, "zip")
		f.status = status
		res, _, err := exportService(t, f).ExportConversation(context.Background(), nil,
			ExportConversationInput{Crew: testCrew, ConversationIDs: []string{convID}})
		if err != nil || res == nil || !res.IsError || !strings.Contains(textOf(res), want) {
			t.Errorf("status %d: res = %+v, want a tool error naming %q", status, res, want)
		}
		f.srv.Close()
	}
}

func TestExportRefusesBadRequestsBeforeCallingTheOperator(t *testing.T) {
	f := newFakeOperator(oneReport, "zip")
	defer f.srv.Close()
	svc := exportService(t, f)
	many := make([]string, maxExportIDs+1)
	for i := range many {
		many[i] = "id"
	}
	cases := map[string]ExportConversationInput{
		"no ids":       {Crew: testCrew},
		"blank ids":    {Crew: testCrew, ConversationIDs: []string{" ", ""}},
		"too many ids": {Crew: testCrew, ConversationIDs: many},
		"unknown crew": {Crew: "nobody", ConversationIDs: []string{convID}},
	}
	for name, in := range cases {
		res, _, err := svc.ExportConversation(context.Background(), nil, in)
		if err != nil || res == nil || !res.IsError {
			t.Errorf("%s: res = %+v, err = %v, want a tool error", name, res, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("the operator was called %v for a bad request", f.calls)
	}
}

func TestExportWithoutAnExporterSaysItIsNotConfigured(t *testing.T) {
	svc := NewService(staticLister{crews: []Crew{{Name: testCrew, Namespace: "ns"}}}, NewGateway(), NewStore(time.Hour), 1)
	res, _, _ := svc.ExportConversation(context.Background(), nil, ExportConversationInput{Crew: testCrew, ConversationIDs: []string{"c"}})
	if res == nil || !res.IsError || !strings.Contains(textOf(res), "not configured") {
		t.Errorf("res = %+v", res)
	}
}

func TestExportReportsAnUnreachableOperatorAndAMissingToken(t *testing.T) {
	crews := staticLister{crews: []Crew{{Name: testCrew, Namespace: "ns"}}}
	in := ExportConversationInput{Crew: testCrew, ConversationIDs: []string{"c"}}
	down := NewService(crews, NewGateway(), NewStore(time.Hour), 1).
		WithExporter(NewExporterWith("http://127.0.0.1:1", http.DefaultClient, func() (string, error) { return "t", nil }))
	if res, _, _ := down.ExportConversation(context.Background(), nil, in); res == nil || !strings.Contains(textOf(res), "unreachable") {
		t.Errorf("unreachable operator: res = %+v", res)
	}
	f := newFakeOperator(oneReport, "zip")
	defer f.srv.Close()
	noToken := NewService(crews, NewGateway(), NewStore(time.Hour), 1).
		WithExporter(NewExporterWith(f.srv.URL, f.srv.Client(), func() (string, error) { return "", errors.New("no file") }))
	if res, _, _ := noToken.ExportConversation(context.Background(), nil, in); res == nil || !strings.Contains(textOf(res), "service account token") || len(f.calls) != 0 {
		t.Errorf("no token: res = %+v, calls = %v", res, f.calls)
	}
}

func TestExportThroughTheMCPProtocol(t *testing.T) {
	f := newFakeOperator(oneReport, zipOf(oneReport))
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandler(exportService(t, f), "s3cret"))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connectSession(ctx, t, srv.URL+"/mcp", "s3cret")
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "export_conversation", Arguments: map[string]any{"crew": testCrew, "conversationIds": []string{convID}},
	})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	resource, ok := res.Content[0].(*mcp.EmbeddedResource)
	if !ok || string(resource.Resource.Blob) != zipOf(oneReport) {
		t.Errorf("content = %+v", res.Content)
	}
}

func TestTheTicketNamesItsConversationSoItCanBeExported(t *testing.T) {
	tk := NewStore(time.Hour).Create(testCrew, "ns", "conv-9", "q")
	if got := tk.Snapshot().Conversation; got != "conv-9" {
		t.Errorf("conversation = %q", got)
	}
}

func TestTheExporterNeverFollowsARedirectWithItsToken(t *testing.T) {
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere++ }))
	defer other.Close()
	op := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer op.Close()
	crews := staticLister{crews: []Crew{{Name: testCrew, Namespace: "ns"}}}
	exporter := NewExporterWith(op.URL, NewExporter(op.URL).client, func() (string, error) { return "sa-token", nil })
	svc := NewService(crews, NewGateway(), NewStore(time.Hour), 1).WithExporter(exporter)
	res, _, _ := svc.ExportConversation(context.Background(), nil, ExportConversationInput{Crew: testCrew, ConversationIDs: []string{"c"}})
	if res == nil || !res.IsError || elsewhere != 0 {
		t.Errorf("res = %+v, the redirect target was called %d times", res, elsewhere)
	}
}
