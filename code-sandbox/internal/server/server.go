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

	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/runner"
)

// toolExecute is the name of the tool that runs a program.
const toolExecute = "execute_code"

// Server holds the runner the tools use.
type Server struct {
	Runner *runner.Runner
	// Inputs finds the stored results a program names in execute_code's inputs.
	Inputs inputs.Resolver
}

// CodeIn is the input of both tools.
type CodeIn struct {
	Language string `json:"language" jsonschema:"the language of the code"`
	Code     string `json:"code" jsonschema:"the source code"`
	Filename string `json:"filename,omitempty" jsonschema:"an optional name for the program, shown in the result"`
}

// ExecuteIn is the input of execute_code: the program and the stored results it reads.
type ExecuteIn struct {
	CodeIn
	Inputs []string `json:"inputs,omitempty" jsonschema:"keys of this discussion's stored results the program reads in full, like k8s-config/001-resources_get; each is the file ./inputs/<key>"` //nolint:lll // one struct tag
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

// executeCodeDescription is what a model reads about execute_code. It says where
// the data comes from (the code itself, or the stored results of this discussion that the
// program reads in full by key) and what the program cannot reach, so a model does not
// expect the sandbox to fetch data and does not count over a few rows it pasted.
const executeCodeDescription = "Run a short program and return its stdout, stderr, and exit code. " +
	"Python 3 with the standard library only, or bash with coreutils, " +
	"including date and the time zone database for time-zone conversion and date arithmetic. " +
	"A program can read a stored result of this discussion in full: name its key in inputs, " +
	"for example inputs: [\"k8s-config/001-resources_get\"], and the program opens the file " +
	"./inputs/k8s-config/001-resources_get. Count, filter and total over all of that file, " +
	"not over rows you copied from a slice; a result in a format the file has (CSV text inside YAML, JSON) " +
	"is parsed by the program itself. Only this discussion's results can be named; a key of another " +
	"discussion or one that does not exist is refused. " +
	"The call itself, this program included, is kept in the discussion too: its key is the result's key " +
	"followed by .call (the call= key on its [CALL ...] line), and naming it in inputs opens the program " +
	"a colleague ran. " +
	"It cannot fetch data: no network, no cluster API, no kubectl, and no credentials. " +
	"Nothing arrives on stdin."

// MCP builds the MCP server with both tools registered.
func (s *Server) MCP() (*mcp.Server, error) {
	schema, err := inputSchema()
	if err != nil {
		return nil, err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "code-sandbox", Version: "v1"}, nil)
	execSchema, err := executeSchema(schema)
	if err != nil {
		return nil, err
	}
	mcp.AddTool(srv, &mcp.Tool{Name: toolExecute, InputSchema: execSchema,
		Description: executeCodeDescription}, s.execute)
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

// executeSchema is the ExecuteIn schema with the language limited to the supported ones.
func executeSchema(base *jsonschema.Schema) (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[ExecuteIn](nil)
	if err != nil {
		return nil, err
	}
	schema.Properties["language"].Enum = base.Properties["language"].Enum
	return schema, nil
}

func (s *Server) execute(
	ctx context.Context, req *mcp.CallToolRequest, in ExecuteIn,
) (*mcp.CallToolResult, RunOut, error) {
	var meta map[string]any
	if req != nil && req.Params != nil {
		meta = req.Params.GetMeta()
	}
	prefix := inputs.PrefixFromMeta(meta)
	staged, err := s.Inputs.Resolve(prefix, in.Inputs)
	if err != nil {
		return nil, RunOut{}, err
	}
	defer inputs.Close(staged)
	res, err := s.Runner.Execute(ctx, in.Language, in.Code, runner.WithInputs(staged), runner.WithThread(prefix))
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
