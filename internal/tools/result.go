package tools

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func jsonResult(value any) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode tool response: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
}

func errorResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, IsError: true}
}

func failure(prefix string, err error) *mcp.CallToolResult {
	return errorResult(prefix + ": " + err.Error())
}

func decodeArguments(request *mcp.CallToolRequest, destination any) error {
	arguments := request.Params.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	return json.Unmarshal(arguments, destination)
}

func stringArgument(request *mcp.CallToolRequest, name string) (string, error) {
	var arguments map[string]json.RawMessage
	if err := decodeArguments(request, &arguments); err != nil {
		return "", err
	}
	encoded, exists := arguments[name]
	if !exists {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil {
		return "", fmt.Errorf("argument %s must be a string", name)
	}
	return value, nil
}

func localInputSchema(properties map[string]any, required ...string) map[string]any {
	properties = maps.Clone(properties)
	if properties == nil {
		properties = make(map[string]any)
	}
	properties["org"] = map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: orgArgDescription}
	return map[string]any{jsonSchemaTypeKey: jsonTypeObject, "properties": properties, "required": append([]string{"org"}, required...)}
}
