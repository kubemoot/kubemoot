// Package mcpserver exposes the bounded artifact read-ops as an MCP stdio server,
// so any network-capable agent (analyst, RAG, coordinator) can sip a slice of a
// referenced artifact without the whole object entering its LLM context. It is the
// shared-service half of artifact-access (the other half is the sandbox
// materializer sidecar). Each tool takes the artifact's object key, fetches the
// object once (streamed), runs one read-op near the data, and returns only the
// slice - capped so a tool can never flood the model context.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/artifact-access/internal/readops"
	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
	"github.com/kubemoot/kubemoot/artifact-access/internal/threadkey"
	"github.com/kubemoot/kubemoot/artifact-access/internal/workspace"
)

const (
	defaultMaxOutputBytes  = 64 * 1024
	defaultJqMaxInputBytes = 8 << 20
	defaultLines           = 20
	defaultGrepMax         = 100
	defaultRowsLimit       = 100
)

// Server holds the artifact read-ops service configuration.
type Server struct {
	Fetcher store.Getter
	// MaxOutputBytes is the per-tool output cap in bytes; <= 0 disables capping.
	MaxOutputBytes int
	// JqMaxInputBytes caps the JSON input jq will parse; <= 0 disables the cap.
	JqMaxInputBytes int64
	// Query answers workspace_query. When nil the tool is not offered.
	Query Querier
	// Warnf logs a deprecation warning; nil means the standard logger.
	Warnf func(format string, args ...any)

	warned sync.Map
}

// Querier runs read-only SQL over named artifacts and returns the rendered rows.
// artifacts maps a table name to an artifact object key.
type Querier interface {
	Query(ctx context.Context, artifacts map[string]string, sql string) (string, error)
	// Hint is one sentence on the engine's SQL dialect, added to the tool description.
	Hint() string
}

// Describer is a Querier that can also say what the tables of named artifacts hold: their
// columns, the types the engine gives them, and example values. workspace_describe is offered
// when the Querier is one.
type Describer interface {
	Describe(ctx context.Context, artifacts map[string]string) (string, error)
}

// New builds a Server with sane defaults around the given fetcher.
func New(f store.Getter) *Server {
	return &Server{
		Fetcher:         f,
		MaxOutputBytes:  defaultMaxOutputBytes,
		JqMaxInputBytes: defaultJqMaxInputBytes,
	}
}

// Out is the structured result every read-op tool returns.
type Out struct {
	Output string `json:"output" jsonschema:"the slice of the artifact the read-op produced"`
}

// MCP builds the MCP server with every read-op tool registered.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "artifact-access", Version: "v1"}, nil)
	s.addTextTools(srv)
	s.addRetiredTools(srv)
	if s.Query != nil {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        toolQuery,
			Description: queryDescription + " " + s.Query.Hint(),
			InputSchema: queryInputSchema(),
		}, s.query)
	}
	s.addCatalogTools(srv)
	return srv
}

// Tool names the package refers to more than once.
const (
	toolQuery        = "workspace_query"
	toolHead         = "workspace_head"
	toolTail         = "workspace_tail"
	toolGrep         = "workspace_grep"
	toolArtifactHead = "artifact_head"
	toolArtifactTail = "artifact_tail"
	toolArtifactGrep = "artifact_grep"
	argLines         = "lines"
)

// The text read tools are served under both names while the old names are retired: the
// workspace_* tool is the one to use, and the artifact_* tool is the same handler and says so.
const (
	headDescription = "First N lines of a stored result, by object key, ending with a " +
		"position line that gives its total line count."
	tailDescription = "Last N lines of a stored result, by object key."
	grepDescription = "Lines of a stored result matching an RE2 regex (capped)."
)

// addTextTools registers workspace_head, workspace_tail and workspace_grep and, beside them,
// artifact_head, artifact_tail and artifact_grep, which keep working and log the first call.
func (s *Server) addTextTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: toolHead, Description: headDescription}, s.head)
	mcp.AddTool(srv, &mcp.Tool{Name: toolTail, Description: tailDescription}, s.tail)
	mcp.AddTool(srv, &mcp.Tool{Name: toolGrep, Description: grepDescription}, s.grep)
	mcp.AddTool(srv, &mcp.Tool{Name: toolArtifactHead,
		Description: renamedNotice(toolHead) + headDescription},
		deprecated(s, toolArtifactHead, toolHead, s.head))
	mcp.AddTool(srv, &mcp.Tool{Name: toolArtifactTail,
		Description: renamedNotice(toolTail) + tailDescription},
		deprecated(s, toolArtifactTail, toolTail, s.tail))
	mcp.AddTool(srv, &mcp.Tool{Name: toolArtifactGrep,
		Description: renamedNotice(toolGrep) + grepDescription},
		deprecated(s, toolArtifactGrep, toolGrep, s.grep))
}

// addRetiredTools registers the four tools workspace_query replaces. They keep working for one
// release; their descriptions and the first call of each say to use workspace_query.
func (s *Server) addRetiredTools(srv *mcp.Server) {
	const use = toolQuery
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_count",
		Description: retiredNotice + "Number of DATA rows in an artifact: skips blank lines and a " +
			"leading column-header row (e.g. a kubectl / resources_list table), so " +
			"the result is the entity count (how many namespaces/pods/rows), not the " +
			"raw line count. For a stored tool call whose result is a JSON list it is " +
			"the number of items in the list."}, deprecated(s, "artifact_count", use, s.count))
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_rows",
		Description: retiredNotice + "Lines [start, start+limit) of an artifact (0-based), ending " +
			"with a position line such as [lines 0-99 of 412]. limit defaults to 100; " +
			"pass a larger limit, or 0 for the rest of the artifact, to read more in " +
			"one call (output is capped at 64 KB)."}, deprecated(s, "artifact_rows", use, s.rows))
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_select",
		Description: retiredNotice + "Project named columns from a table artifact (kubectl table, Markdown table or CSV) " +
			"and return them as CSV. Columns are named as workspace_query names them: lowercase, with " +
			"underscores for spaces and hyphens; the printed title and any case also match. A missing " +
			"column lists the columns the table has. For JSON use artifact_jq; for other text use " +
			"artifact_grep or artifact_rows."}, deprecated(s, "artifact_select", use, s.selectCSV))
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_jq",
		Description: retiredNotice + "Run a jq program over a JSON artifact (input size capped)."},
		deprecated(s, "artifact_jq", use, s.jq))
}

// retiredNotice opens the description of a tool that workspace_query replaces.
const retiredNotice = "Deprecated: use workspace_query, which replaces artifact_count, artifact_rows, " +
	"artifact_select and artifact_jq; this tool is removed in the next release. "

// renamedNotice opens the description of an old name for a tool that has a new one.
func renamedNotice(use string) string {
	return "Deprecated: the same tool as " + use + "; use " + use + ". This name is removed in the next release. "
}

// deprecated wraps a handler so the first call under an old name logs that the name is
// deprecated and what to use, once per tool for the life of the process.
func deprecated[In any](s *Server, old, use string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		s.noteDeprecated(old, use)
		return h(ctx, req, in)
	}
}

// noteDeprecated logs a deprecated tool name the first time it is called.
func (s *Server) noteDeprecated(old, use string) {
	if _, seen := s.warned.LoadOrStore(old, true); seen {
		return
	}
	warn := s.Warnf
	if warn == nil {
		warn = log.Printf
	}
	warn("tool %s is deprecated and is removed in the next release; use %s", old, use)
}

// addCatalogTools registers the tools that tell an agent what the discussion has gathered:
// workspace_list when the store can list objects, workspace_describe when the engine can describe.
func (s *Server) addCatalogTools(srv *mcp.Server) {
	if _, ok := s.Fetcher.(store.Lister); ok {
		mcp.AddTool(srv, &mcp.Tool{Name: "workspace_list", Description: listDescription}, s.list)
	}
	if _, ok := s.Query.(Describer); ok {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "workspace_describe",
			Description: describeDescription,
			InputSchema: describeInputSchema(),
		}, s.describe)
	}
}

const listDescription = "List what this discussion has stored: one line per tool call with its key, tool, " +
	"arguments, content type, rows, bytes, whether it failed and when it started. Only this thread's " +
	"calls are listed. Pass tool to list the calls of one tool. Read a call by its key with " +
	"workspace_query, workspace_head, workspace_tail or workspace_grep."

const describeDescription = "Say what the tables of named artifacts hold, before writing a query: for each " +
	"table its columns, the type the query engine gives each, how many values are null, and a few example " +
	"values. Give artifacts as a map from a table name you choose to an artifact key, the same map " +
	"workspace_query takes (a key may be short, or end in * to read several calls as one table). Types and " +
	"examples are read from the data; no unit is known."

const queryDescription = "Run one read-only SQL SELECT over artifacts. Give artifacts as a map " +
	"from a table name you choose to an artifact key, and write the SQL against those table names. " +
	"Reads JSON lists, kubectl get -o json output (one row per item), kubectl tables, YAML, Markdown " +
	"tables, CSV, and any other text (columns line_no, line). Use it to count, filter, sort, group " +
	"and total instead of doing it by eye. Rows come back as 'a | b' lines, cut at a row limit, with a " +
	"row count line. A tooler's [tool] sections are read one by one: same-shaped ones are one table, " +
	"others are name_2, name_3. A tooler's [CALL key=...] line names one stored tool call: that key is one " +
	"table. A key with * reads every call that matches as one table (same-shaped calls are stacked, failed " +
	"calls are left out), for example <prefix>*-pods_list with the prefix from the [WORKSPACE ...] line. " +
	"Each row of such a table also has _call (the call's key), _args (its arguments as JSON text) and, for an " +
	"argument that is a plain value and differs between the calls, _arg_<name> (for example _arg_vmid), " +
	"so GROUP BY _call or _arg_vmid counts and totals per call. " +
	"A key may be written short, <agent>/<nnn>-<tool> as the file descriptions show it (for example " +
	"k8s-workloads/002-resources_list); it is read inside the current thread, and a key of another " +
	"thread is refused. " +
	"Column names of a table are lowercase with underscores: a kubectl title NODE SELECTOR " +
	"is node_selector, UP-TO-DATE is up_to_date. A table's real column names are on the [CALL ...] line of " +
	"its call (columns= for a table, fields= for JSON) and in workspace_describe; a query that names a " +
	"column the table lacks fails with the table's columns listed, so use those names and never a guess. " +
	"No writes, no files outside the named artifacts, no network."

// --- tool inputs ---

type headIn struct {
	Key   string `json:"key" jsonschema:"the artifact object key (bucket-relative)"`
	Lines int    `json:"lines,omitempty" jsonschema:"how many lines (default 20)"`
}
type tailIn struct {
	Key   string `json:"key" jsonschema:"the artifact object key (bucket-relative)"`
	Lines int    `json:"lines,omitempty" jsonschema:"how many lines (default 20)"`
}
type grepIn struct {
	Key     string `json:"key" jsonschema:"the artifact object key"`
	Pattern string `json:"pattern" jsonschema:"an RE2 regular expression"`
	Max     int    `json:"max,omitempty" jsonschema:"max matching lines (default 100)"`
}
type keyIn struct {
	Key string `json:"key" jsonschema:"the artifact object key"`
}
type rowsIn struct {
	Key   string `json:"key" jsonschema:"the artifact object key"`
	Start int    `json:"start,omitempty" jsonschema:"0-based first line (default 0)"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many lines (default 100)"`
}
type selectIn struct {
	Key       string   `json:"key" jsonschema:"the artifact object key"`
	Columns   []string `json:"columns" jsonschema:"column names to project, in output order"`
	Delimiter string   `json:"delimiter,omitempty" jsonschema:"single-char field delimiter (default comma)"`
}
type queryIn struct {
	Artifacts artifactMap `json:"artifacts" jsonschema:"table name you choose, mapped to the artifact key"`
	SQL       string      `json:"sql" jsonschema:"one SELECT over those table names"`
}
type describeIn struct {
	Artifacts artifactMap `json:"artifacts" jsonschema:"table name you choose, mapped to the artifact key"`
}
type listIn struct {
	Tool string `json:"tool,omitempty" jsonschema:"list only the calls of this tool"`
}
type jqIn struct {
	Key     string `json:"key" jsonschema:"the artifact object key"`
	Program string `json:"program" jsonschema:"a jq program, e.g. .items[].name"`
}

// --- tool handlers ---

func (s *Server) head(ctx context.Context, req *mcp.CallToolRequest, in headIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, scopeOf(req), in.Key, func(r io.Reader) (string, error) {
		return readops.Head(r, orDefault(in.Lines, defaultLines), s.MaxOutputBytes)
	})
}

func (s *Server) tail(ctx context.Context, req *mcp.CallToolRequest, in tailIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, scopeOf(req), in.Key, func(r io.Reader) (string, error) {
		return readops.Tail(r, orDefault(in.Lines, defaultLines), s.MaxOutputBytes)
	})
}

func (s *Server) grep(ctx context.Context, req *mcp.CallToolRequest, in grepIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, scopeOf(req), in.Key, func(r io.Reader) (string, error) {
		return readops.Grep(r, in.Pattern, orDefault(in.Max, defaultGrepMax), s.MaxOutputBytes)
	})
}

func (s *Server) count(ctx context.Context, req *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, Out, error) {
	sc := scopeOf(req)
	key, err := sc.Resolve(in.Key)
	if err != nil {
		return nil, Out{}, err
	}
	if n, ok := s.recordedRows(ctx, key); ok {
		return s.result(strconv.FormatInt(n, 10) + "\n")
	}
	return s.read(ctx, sc, key, func(r io.Reader) (string, error) {
		n, err := readops.CountData(r)
		return strconv.FormatInt(n, 10) + "\n", err
	})
}

func (s *Server) rows(ctx context.Context, req *mcp.CallToolRequest, in rowsIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, scopeOf(req), in.Key, func(r io.Reader) (string, error) {
		return readops.Rows(r, in.Start, orDefault(in.Limit, defaultRowsLimit), s.MaxOutputBytes)
	})
}

func (s *Server) selectCSV(
	ctx context.Context, req *mcp.CallToolRequest, in selectIn,
) (*mcp.CallToolResult, Out, error) {
	sc := scopeOf(req)
	hint := s.recordedFormat(ctx, sc, in.Key)
	return s.read(ctx, sc, in.Key, func(r io.Reader) (string, error) {
		return readops.SelectCSV(r, hint, in.Columns, delimiterOrComma(in.Delimiter), s.MaxOutputBytes)
	})
}

func (s *Server) jq(ctx context.Context, req *mcp.CallToolRequest, in jqIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, scopeOf(req), in.Key, func(r io.Reader) (string, error) {
		return readops.Jq(r, in.Program, s.JqMaxInputBytes, s.MaxOutputBytes)
	})
}

func (s *Server) query(ctx context.Context, req *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, Out, error) {
	artifacts, err := scopeOf(req).ResolveAll(in.Artifacts)
	if err != nil {
		return nil, Out{}, err
	}
	text, err := s.Query.Query(ctx, artifacts, in.SQL)
	if err != nil {
		return nil, Out{}, err
	}
	return s.result(text)
}

func (s *Server) list(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, Out, error) {
	l, ok := s.Fetcher.(store.Lister)
	if !ok {
		return nil, Out{}, fmt.Errorf("this store cannot list objects")
	}
	text, err := workspace.Catalog(ctx, l, scopeOf(req).Prefix(), in.Tool)
	if err != nil {
		return nil, Out{}, err
	}
	return s.result(text)
}

func (s *Server) describe(
	ctx context.Context, req *mcp.CallToolRequest, in describeIn,
) (*mcp.CallToolResult, Out, error) {
	d, ok := s.Query.(Describer)
	if !ok {
		return nil, Out{}, fmt.Errorf("this engine cannot describe tables")
	}
	sc := scopeOf(req)
	if sc.Prefix() == "" {
		return nil, Out{}, workspace.ErrNoThread
	}
	artifacts, err := sc.ResolveAll(in.Artifacts)
	if err != nil {
		return nil, Out{}, err
	}
	if err := inThread(sc, artifacts); err != nil {
		return nil, Out{}, err
	}
	text, err := d.Describe(ctx, artifacts)
	if err != nil {
		return nil, Out{}, err
	}
	return s.result(text)
}

// inThread refuses a key that, after resolving, does not lie under the thread's prefix (a key of
// one segment, or a bare "*", is passed through by Resolve), so a describe never reaches past the thread.
func inThread(sc threadkey.Scope, artifacts map[string]string) error {
	for table, key := range artifacts {
		if !strings.HasPrefix(key, sc.Prefix()) {
			return fmt.Errorf("artifacts[%q]: key %q is not a key of this thread; use a key like %s",
				table, key, "k8s-workloads/002-resources_list")
		}
	}
	return nil
}

// recordedRows is the row count the agent runtime recorded in the headers of a stored tool call
// whose result is a JSON list. It is the entity count a single-line JSON body would hide from a
// line count. False for any other object, and for a result that was cut.
func (s *Server) recordedRows(ctx context.Context, key string) (int64, bool) {
	d, ok := s.Fetcher.(store.Describer)
	if !ok || key == "" {
		return 0, false
	}
	meta, err := d.Describe(ctx, key)
	if err != nil {
		return 0, false
	}
	return workspace.RowsHeader(meta)
}

// recordedFormat is the format the Content-Type header of the stored object names; empty when
// the key does not resolve, the store keeps no headers, or the object has none.
func (s *Server) recordedFormat(ctx context.Context, sc threadkey.Scope, key string) workspace.Format {
	d, ok := s.Fetcher.(store.Describer)
	if !ok || key == "" {
		return ""
	}
	resolved, err := sc.Resolve(key)
	if err != nil {
		return ""
	}
	meta, err := d.Describe(ctx, resolved)
	if err != nil {
		return ""
	}
	return workspace.HintOf(meta)
}

// result caps the text and wraps it as a tool result.
func (s *Server) result(text string) (*mcp.CallToolResult, Out, error) {
	text = s.capOutput(text)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, Out{Output: text}, nil
}

// scopeOf is the thread a tool call runs in, from the _meta the agent runtime sends; the zero
// scope (no thread) when the call has none.
func scopeOf(req *mcp.CallToolRequest) threadkey.Scope {
	if req == nil || req.Params == nil {
		return threadkey.Scope{}
	}
	return threadkey.FromMeta(req.Params.GetMeta())
}

// read resolves the key within the call's thread, fetches the object once, runs the read-op, and
// wraps the capped slice as a tool result. A missing key, a key of another thread, or a
// fetch/read error is returned as a tool error so the model can see it and self-correct (the SDK
// marks IsError).
func (s *Server) read(
	ctx context.Context, sc threadkey.Scope, key string, op func(io.Reader) (string, error),
) (*mcp.CallToolResult, Out, error) {
	if key == "" {
		return nil, Out{}, fmt.Errorf("key is required")
	}
	key, err := sc.Resolve(key)
	if err != nil {
		return nil, Out{}, err
	}
	rc, err := s.Fetcher.Get(ctx, key)
	if err != nil {
		return nil, Out{}, fmt.Errorf("fetch %s: %w", key, err)
	}
	defer func() { _ = rc.Close() }()

	text, err := op(rc)
	if err != nil {
		return nil, Out{}, err
	}
	return s.result(text)
}

// capOutput bounds a result so a read-op can never flood the model context. It cuts
// at the last whole line within the cap (line-oriented output stays clean) and notes
// the truncation rather than silently dropping data.
func (s *Server) capOutput(text string) string {
	if s.MaxOutputBytes <= 0 || len(text) <= s.MaxOutputBytes {
		return text
	}
	cut := strings.LastIndexByte(text[:s.MaxOutputBytes], '\n')
	if cut <= 0 {
		// No line break within the cap: back off to a valid UTF-8 boundary so we
		// never forward a half-rune to the model.
		cut = s.MaxOutputBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
	}
	return text[:cut] + "\n[truncated at " + strconv.Itoa(s.MaxOutputBytes) + " bytes; narrow the query]"
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// delimiterOrComma returns the first rune of d, or comma when d is empty.
func delimiterOrComma(d string) rune {
	if d == "" {
		return ','
	}
	return []rune(d)[0]
}
