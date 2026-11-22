package tools

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type registeredTool struct {
	Tool    *mcp.Tool
	Handler mcp.ToolHandler
}

type testRegistrar struct{ tools map[string]*registeredTool }

func newTestRegistrar() *testRegistrar {
	return &testRegistrar{tools: make(map[string]*registeredTool)}
}
func (r *testRegistrar) AddTool(tool *mcp.Tool, handler mcp.ToolHandler) {
	r.tools[tool.Name] = &registeredTool{Tool: tool, Handler: handler}
}
func (r *testRegistrar) GetTool(name string) *registeredTool   { return r.tools[name] }
func (r *testRegistrar) ListTools() map[string]*registeredTool { return r.tools }

func testRequest(name string, arguments map[string]any) *mcp.CallToolRequest {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		panic(err)
	}
	if arguments == nil {
		encoded = []byte("{}")
	}
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: encoded}}
}

func testSchema(t *testing.T, tool *mcp.Tool) struct {
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required"`
} {
	t.Helper()
	encoded, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	require.NoError(t, json.Unmarshal(encoded, &schema))
	return schema
}
