// Package server exposes the runner as an MCP server with two tools,
// execute_code and validate_code.
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/javajon/kubemoot/code-sandbox/internal/runner"
)

// Server holds the runner the tools use.
type Server struct {
	Runner *runner.Runner
}

// CodeIn is the input of both tools.
type CodeIn struct {
	Language string `json:"language" jsonschema:"the language of the code"`
	Code     string `json:"code" jsonschema:"the source code"`
	Filename string `json:"filename,omitempty" jsonschema:"an optional name for the program, shown in the result"`
}

// RunOut is the structured result of execute_code.
type RunOut struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	TimedOut bool   `json:"timedOut"`
}

// CheckOut is the structured result of validate_code.
type CheckOut struct {
	Valid  bool   `json:"valid"`
	Errors string `json:"errors,omitempty"`
}

// MCP builds the MCP server with both tools registered.
func (s *Server) MCP() (*mcp.Server, error) {
	schema, err := inputSchema()
	if err != nil {
		return nil, err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "code-sandbox", Version: "v1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "execute_code", InputSchema: schema,
		Description: "Run a short program and return its stdout, stderr, and exit code. " +
			"Python 3 with the standard library only, or bash. No network. " +
			"Nothing arrives on stdin: put the data in the code or open a file under /artifacts."}, s.execute)
	mcp.AddTool(srv, &mcp.Tool{Name: "validate_code", InputSchema: schema,
		Description: "Check a program's syntax without running it."}, s.validate)
	return srv, nil
}

// inputSchema is the CodeIn schema with the language limited to the supported ones.
func inputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[CodeIn](nil)
	if err != nil {
		return nil, err
	}
	enum := make([]any, 0, len(runner.Languages))
	for _, name := range runner.Names() {
		enum = append(enum, name)
	}
	schema.Properties["language"].Enum = enum
	return schema, nil
}

func (s *Server) execute(ctx context.Context, _ *mcp.CallToolRequest, in CodeIn) (*mcp.CallToolResult, RunOut, error) {
	res, err := s.Runner.Execute(ctx, in.Language, in.Code)
	if err != nil {
		return nil, RunOut{}, err
	}
	out := RunOut{Stdout: clean(res.Stdout), Stderr: clean(res.Stderr), ExitCode: res.ExitCode, TimedOut: res.TimedOut}
	return &mcp.CallToolResult{Content: runContent(out, res, in.Filename, s.Runner.Timeout)}, out, nil
}

func (s *Server) validate(
	ctx context.Context, _ *mcp.CallToolRequest, in CodeIn,
) (*mcp.CallToolResult, CheckOut, error) {
	res, err := s.Runner.Validate(ctx, in.Language, in.Code)
	if err != nil {
		return nil, CheckOut{}, err
	}
	if res.ExitCode == 0 && !res.TimedOut {
		return text("Syntax is valid.", false), CheckOut{Valid: true}, nil
	}
	errs := clean(res.Stderr)
	return text("Syntax error:\n"+errs, true), CheckOut{Errors: errs}, nil
}

// runContent lays out a run as text blocks: stdout, stderr, then a summary.
func runContent(out RunOut, res runner.Result, filename string, timeout time.Duration) []mcp.Content {
	var blocks []mcp.Content
	if out.Stdout != "" {
		blocks = append(blocks, &mcp.TextContent{Text: out.Stdout})
	}
	if out.Stderr != "" {
		blocks = append(blocks, &mcp.TextContent{Text: "stderr:\n" + out.Stderr})
	}
	return append(blocks, &mcp.TextContent{Text: summary(res, filename, timeout)})
}

func summary(res runner.Result, filename string, timeout time.Duration) string {
	lines := []string{fmt.Sprintf("Exit code: %d", res.ExitCode),
		fmt.Sprintf("Execution time: %.2fs", res.Duration.Seconds())}
	if filename != "" {
		lines = append(lines, "File: "+filename)
	}
	if res.TimedOut {
		lines = append(lines, fmt.Sprintf("Stopped after %s: the program did not finish in time.", timeout))
	}
	if res.Truncated {
		lines = append(lines, "Output was cut short; print less.")
	}
	return strings.Join(lines, "\n")
}

func text(s string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}, IsError: isError}
}

// clean trims surrounding whitespace and repairs a rune cut by the output cap.
func clean(s string) string {
	return strings.ToValidUTF8(strings.TrimSpace(s), "")
}
