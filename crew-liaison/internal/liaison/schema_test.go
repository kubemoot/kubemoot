package liaison

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolArgumentDescriptions pins the argument descriptions MCP clients see for
// the arguments whose descriptions are set on the schema rather than in a tag.
func TestToolArgumentDescriptions(t *testing.T) {
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := NewMCPServer(&Service{}).Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"ask": {
			argNamespace: "namespace of the crew, needed only when the name is not unique",
			argWaitSeconds: "seconds to wait for the answer before returning the ticket; " +
				"default and maximum 45, 0 returns the ticket at once",
		},
		"get_answer": {
			argWaitSeconds: "seconds to wait for the answer before reporting pending; " +
				"default and maximum 45, 0 reports the current state at once",
		},
	}
	for _, tool := range tools.Tools {
		for arg, desc := range want[tool.Name] {
			if got := propertyDescription(t, tool, arg); got != desc {
				t.Errorf("%s.%s description = %q, want %q", tool.Name, arg, got, desc)
			}
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Errorf("tools not listed: %v", want)
	}
}

// propertyDescription reads the description of property arg from the tool's
// input schema as a client receives it.
func propertyDescription(t *testing.T, tool *mcp.Tool, arg string) string {
	t.Helper()
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("%s input schema is %T", tool.Name, tool.InputSchema)
	}
	props, _ := schema["properties"].(map[string]any)
	prop, _ := props[arg].(map[string]any)
	desc, _ := prop["description"].(string)
	return desc
}

func TestInputSchemaRejectsUnknownProperty(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a description for a property the type lacks must panic")
		}
	}()
	inputSchema[GetAnswerInput](map[string]string{"nope": "x"})
}
