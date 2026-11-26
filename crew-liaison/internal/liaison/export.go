package liaison

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxExportIDs is how many conversations one call may export; the operator enforces the same
// limit. The liaison does not import the operator's export builder or crewscope, which would bring
// the OMPI schema and the NATS client into its binary, so this limit and the download name are
// repeated, and the operator is the one place an id is checked.
const maxExportIDs = 50

// maxInlineExport is the largest zip the tool puts in its result. A tool result is read by
// a model client that caps what it takes in (Claude Code's default is about 25,000 tokens,
// and base64 counts as text, at about 4 characters a token), so a larger zip is not returned
// inline: the result carries the manifest and says how to take the file instead. The liaison
// reads at most this much of a zip and keeps none of it.
const maxInlineExport = 48 << 10

// maxReport bounds the manifest read when the zip is too large to return.
const maxReport = 1 << 20

// serviceAccountTokenFile is the projected token the liaison presents to the operator.
const serviceAccountTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Exporter fetches conversation exports from the operator's report service. It presents the
// liaison's service account token, which the operator checks with a TokenReview and a
// SubjectAccessReview for get on the Crew, as it does for the memory export.
type Exporter struct {
	client  *http.Client
	baseURL string
	token   func() (string, error)
}

// NewExporter returns an exporter of the operator report service at baseURL, such as
// http://kubemoot-operator-report.kubemoot.svc:8082. The token is read on every call,
// because the projected token rotates.
func NewExporter(baseURL string) *Exporter {
	return &Exporter{
		client: &http.Client{
			Timeout: 2 * time.Minute,
			// The request carries the service account token, which must not follow a redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		token: func() (string, error) {
			b, err := os.ReadFile(serviceAccountTokenFile)
			return strings.TrimSpace(string(b)), err
		},
	}
}

// NewExporterWithCA returns an exporter of the operator report service at baseURL (https) that
// verifies the service's certificate against the CA bundle in caFile, read again when it changes.
func NewExporterWithCA(baseURL, caFile string) (*Exporter, error) {
	transport, err := newCATransport(caFile)
	if err != nil {
		return nil, err
	}
	e := NewExporter(baseURL)
	e.client.Transport = transport
	return e, nil
}

// NewExporterWith returns an exporter with its own client and token source; tests use it.
func NewExporterWith(baseURL string, client *http.Client, token func() (string, error)) *Exporter {
	return &Exporter{client: client, baseURL: strings.TrimRight(baseURL, "/"), token: token}
}

// exportError is a refusal or failure the operator answered, with its message.
type exportError struct {
	status  int
	message string
}

func (e *exportError) Error() string { return e.message }

// bearer is the credential the operator is shown: the door's assertion of a person it signed in,
// else the caller's own Kubernetes token, else (a caller with neither, such as the shared token or
// no verification) the liaison's own, which the operator refuses on a crew that verifies callers
// and the liaison does not own the conversation of.
func (e *Exporter) bearer(ctx context.Context, crew Crew) (string, error) {
	token, err := credentialFor(ctx, crew.Namespace, crew.Name)
	if err != nil || token != "" {
		return token, err
	}
	if token, err = e.token(); err != nil {
		return "", fmt.Errorf("the liaison has no service account token: %w", err)
	}
	return token, nil
}

// fetch asks the operator for the export of ids in a crew, as the zip or, with report, its
// manifest. It reads at most limit bytes and reports whether there was more.
func (e *Exporter) fetch(
	ctx context.Context, crew Crew, ids []string, zone string, report bool, limit int64,
) (body []byte, truncated bool, err error) {
	query := url.Values{"id": ids}
	if zone != "" {
		query.Set("timeZone", zone)
	}
	if report {
		query.Set("report", "true")
	}
	endpoint := fmt.Sprintf("%s/conversation-export/%s/%s?%s",
		e.baseURL, url.PathEscape(crew.Namespace), url.PathEscape(crew.Name), query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	token, err := e.bearer(ctx, crew)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("the operator's export service is unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, false, &exportError{status: resp.StatusCode, message: strings.TrimSpace(string(msg))}
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return nil, true, nil
	}
	return body, false, nil
}

// ExportConversationInput names the conversations to export. ConversationIDs takes its description
// from exportInputSchema.
type ExportConversationInput struct {
	Crew            string   `json:"crew" jsonschema:"name of the crew whose conversations to export; see list_crews"`
	Namespace       string   `json:"namespace,omitempty"`
	ConversationIDs []string `json:"conversationIds"`
	TimeZone        string   `json:"timeZone,omitempty"`
}

const conversationIDsDesc = "the conversation ids to export, as ask and get_answer return them; one id returns that " +
	"conversation's zip, several return a zip of zips with a manifest of what is included, refused and expired"

// exportInputSchema is the export_conversation tool's input schema.
func exportInputSchema() *jsonschema.Schema {
	return inputSchema[ExportConversationInput](map[string]string{"conversationIds": conversationIDsDesc})
}

// ExportConversationOutput says what the export holds and whether the zip is in this result.
type ExportConversationOutput struct {
	// Delivered is true when the zip is in this result as an embedded resource.
	Delivered bool   `json:"delivered"`
	Filename  string `json:"filename,omitempty"`
	SizeBytes int    `json:"sizeBytes,omitempty"`
	Note      string `json:"note,omitempty"`
	// Manifest is the export's manifest: what is included, what had expired or is not kept,
	// and for several conversations which are included, refused, expired or failed.
	Manifest map[string]any `json:"manifest"`
}

// ExportConversation returns the zip of one conversation, or a zip of zips of several, as
// an embedded resource, when it is small enough to return; otherwise the manifest and the
// way to take the file. The zip is built by the operator on this request and never stored.
func (s *Service) ExportConversation(
	ctx context.Context, _ *mcp.CallToolRequest, in ExportConversationInput,
) (*mcp.CallToolResult, ExportConversationOutput, error) {
	if s.exporter == nil {
		return toolError("conversation export is not configured on this liaison"), ExportConversationOutput{}, nil
	}
	ids, err := exportIDs(in.ConversationIDs)
	if err != nil {
		return toolError(err.Error()), ExportConversationOutput{}, nil
	}
	crew, err := s.exportCrew(ctx, in)
	if err != nil {
		return toolError(err.Error()), ExportConversationOutput{}, nil
	}
	return s.export(ctx, crew, ids, in.TimeZone)
}

// exportIDs trims the ids and checks there is at least one and no more than the limit.
func exportIDs(raw []string) ([]string, error) {
	ids := make([]string, 0, len(raw))
	for _, id := range raw {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return nil, errors.New("conversationIds is required: the conversation ids to export")
	case len(ids) > maxExportIDs:
		return nil, fmt.Errorf("ask for at most %d conversations at once", maxExportIDs)
	}
	return ids, nil
}

// exportCrew finds the crew. Whether the caller may export each id is the operator's to decide:
// it holds the conversation's owner, and fetch presents the caller's own token to it.
func (s *Service) exportCrew(ctx context.Context, in ExportConversationInput) (Crew, error) {
	crews, err := s.crews.List(ctx)
	if err != nil {
		return Crew{}, err
	}
	crew, err := find(crews, in.Crew, in.Namespace)
	if err != nil {
		return Crew{}, err
	}
	return crew, nil
}

func (s *Service) export(
	ctx context.Context, crew Crew, ids []string, zone string,
) (*mcp.CallToolResult, ExportConversationOutput, error) {
	zipBytes, tooLarge, err := s.exporter.fetch(ctx, crew, ids, zone, false, maxInlineExport)
	if err != nil {
		return toolError(exportFailure(err)), ExportConversationOutput{}, nil
	}
	out := ExportConversationOutput{Manifest: map[string]any{}}
	if tooLarge {
		return s.tooLarge(ctx, crew, ids, zone, out)
	}
	if out.Manifest, err = manifestOf(zipBytes); err != nil {
		return toolError("the operator's export was not understood"), ExportConversationOutput{}, nil
	}
	if len(ids) > 1 && !anyIncluded(out.Manifest) {
		out.Note = "no conversation could be exported; the manifest says why for each"
		return nil, out, nil
	}
	out.Delivered, out.Filename, out.SizeBytes = true, exportFileName(crew, ids), len(zipBytes)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
			URI: "kubemoot-export:///" + out.Filename, MIMEType: "application/zip", Blob: zipBytes,
		}},
	}}, out, nil
}

// tooLarge answers for a zip beyond what a tool result carries: the manifest, which the
// operator builds without the files, and the way to take the zip.
func (s *Service) tooLarge(
	ctx context.Context, crew Crew, ids []string, zone string, out ExportConversationOutput,
) (*mcp.CallToolResult, ExportConversationOutput, error) {
	report, truncated, err := s.exporter.fetch(ctx, crew, ids, zone, true, maxReport)
	if err != nil || truncated {
		return toolError("the export is too large for a tool result and its manifest could not be read"),
			ExportConversationOutput{}, nil
	}
	if err := json.Unmarshal(report, &out.Manifest); err != nil {
		return toolError("the operator's export report was not understood"), ExportConversationOutput{}, nil
	}
	out.Note = fmt.Sprintf("the zip is larger than the %d KiB a tool result carries; take it with "+
		"'kmctl conversation export' or the operator's conversation-export endpoint", maxInlineExport>>10)
	return nil, out, nil
}

// manifestOf reads manifest.json out of an export zip.
func manifestOf(zipBytes []byte) (map[string]any, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		manifest := map[string]any{}
		return manifest, json.NewDecoder(io.LimitReader(rc, maxReport)).Decode(&manifest)
	}
	return nil, errors.New("the export has no manifest")
}

func anyIncluded(manifest map[string]any) bool {
	included, _ := manifest["included"].([]any)
	return len(included) > 0
}

// exportFileName is the name the operator gives the download, for the embedded resource.
func exportFileName(crew Crew, ids []string) string {
	if len(ids) == 1 {
		return fmt.Sprintf("%s-conversation-%s.zip", crew.Name, ids[0])
	}
	return crew.Name + "-conversations.zip"
}

// exportFailure is the message a tool error carries for an operator answer or a transport error.
func exportFailure(err error) string {
	var ee *exportError
	switch {
	case errors.As(err, &ee) && ee.status == http.StatusNotFound:
		return "the conversation is not held: it expired or does not exist in this crew"
	case errors.As(err, &ee) && ee.status >= 400 && ee.status < 500:
		return ee.message
	case errors.As(err, &ee):
		return "the operator could not build the export: " + ee.message
	}
	return err.Error()
}
