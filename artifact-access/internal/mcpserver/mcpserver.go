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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/javajon/kubemoot/artifact-access/internal/readops"
	"github.com/javajon/kubemoot/artifact-access/internal/store"
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
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_head",
		Description: "First N lines of an artifact, by object key."}, s.head)
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_tail",
		Description: "Last N lines of an artifact, by object key."}, s.tail)
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_grep",
		Description: "Lines of an artifact matching an RE2 regex (capped)."}, s.grep)
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_count",
		Description: "Number of DATA rows in an artifact: skips blank lines and a " +
			"leading column-header row (e.g. a kubectl / resources_list table), so " +
			"the result is the entity count (how many namespaces/pods/rows), not the " +
			"raw line count."}, s.count)
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_rows",
		Description: "Lines [start, start+limit) of an artifact (0-based)."}, s.rows)
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_select",
		Description: "Project named columns from a CSV artifact with a header row."}, s.selectCSV)
	mcp.AddTool(srv, &mcp.Tool{Name: "artifact_jq",
		Description: "Run a jq program over a JSON artifact (input size capped)."}, s.jq)
	return srv
}

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
type jqIn struct {
	Key     string `json:"key" jsonschema:"the artifact object key"`
	Program string `json:"program" jsonschema:"a jq program, e.g. .items[].name"`
}

// --- tool handlers ---

func (s *Server) head(ctx context.Context, _ *mcp.CallToolRequest, in headIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		return readops.Head(r, orDefault(in.Lines, defaultLines), s.MaxOutputBytes)
	})
}

func (s *Server) tail(ctx context.Context, _ *mcp.CallToolRequest, in tailIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		return readops.Tail(r, orDefault(in.Lines, defaultLines), s.MaxOutputBytes)
	})
}

func (s *Server) grep(ctx context.Context, _ *mcp.CallToolRequest, in grepIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		return readops.Grep(r, in.Pattern, orDefault(in.Max, defaultGrepMax), s.MaxOutputBytes)
	})
}

func (s *Server) count(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		n, err := readops.CountData(r)
		return strconv.FormatInt(n, 10) + "\n", err
	})
}

func (s *Server) rows(ctx context.Context, _ *mcp.CallToolRequest, in rowsIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		return readops.Rows(r, in.Start, orDefault(in.Limit, defaultRowsLimit), s.MaxOutputBytes)
	})
}

func (s *Server) selectCSV(ctx context.Context, _ *mcp.CallToolRequest, in selectIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		return readops.SelectCSV(r, in.Columns, delimiterOrComma(in.Delimiter), s.MaxOutputBytes)
	})
}

func (s *Server) jq(ctx context.Context, _ *mcp.CallToolRequest, in jqIn) (*mcp.CallToolResult, Out, error) {
	return s.read(ctx, in.Key, func(r io.Reader) (string, error) {
		return readops.Jq(r, in.Program, s.JqMaxInputBytes, s.MaxOutputBytes)
	})
}

// read fetches the keyed object once, runs the read-op, and wraps the capped slice
// as a tool result. A nil key or a fetch/read error is returned as a tool error so
// the model can see it and self-correct (the SDK marks IsError).
func (s *Server) read(
	ctx context.Context, key string, op func(io.Reader) (string, error),
) (*mcp.CallToolResult, Out, error) {
	if key == "" {
		return nil, Out{}, fmt.Errorf("key is required")
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
	text = s.capOutput(text)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, Out{Output: text}, nil
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
