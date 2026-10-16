package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeTools struct{}

func (fakeTools) Specifications() []map[string]any {
	return []map[string]any{{"name": "list_providers"}}
}

func (fakeTools) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	if name == "list_providers" {
		return `{"providers":[]}`, nil
	}
	return "", errors.New("unknown tool")
}

func run(t *testing.T, input string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	s := &Server{Handlers: fakeTools{}, ProtocolVersion: "2024-11-05", ServerName: "test", ServerVersion: "dev"}
	if err := s.Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		responses = append(responses, r)
	}
	return responses
}

func TestServer_speaksTheMCPStdioProtocol(t *testing.T) {
	responses := run(t, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_providers"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
		`not json`,
	}, "\n"))
	if len(responses) != 6 {
		t.Fatalf("a notification gets no response: %d responses", len(responses))
	}
	if responses[0]["result"].(map[string]any)["serverInfo"].(map[string]any)["name"] != "test" {
		t.Fatalf("initialize: %v", responses[0])
	}
	if len(responses[1]["result"].(map[string]any)["tools"].([]any)) != 1 {
		t.Fatalf("tools/list: %v", responses[1])
	}
	if responses[2]["result"].(map[string]any)["isError"] != nil {
		t.Fatalf("tools/call ok: %v", responses[2])
	}
	if responses[3]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("a failing tool is an in-band error: %v", responses[3])
	}
	if responses[4]["error"] == nil || responses[5]["error"] == nil {
		t.Fatalf("unknown method and parse error: %v %v", responses[4], responses[5])
	}
}
