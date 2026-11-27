package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"sync"

	mcpgrafana "github.com/grafana/mcp-grafana/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const orgArgDescription = "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs."

const (
	datasourceUIDArg         = "datasourceUid"
	datasourceUIDArgSnake    = "datasource_uid"
	jsonTypeString           = "string"
	jsonSchemaTypeKey        = "type"
	jsonSchemaDescriptionKey = "description"
	jsonTypeNumber           = "number"
	jsonTypeObject           = "object"
	pageArgument             = "page"
)

const datasourceUIDHint = "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). " +
	"One tenant: gs-{kind}-{tenant}. See list_datasources."

// gfBinder resolves organization access and datasource identity before invoking
// a native upstream handler. Per-request Grafana settings are carried by context.
type gfBinder struct {
	authorizer   authz.Authorizer
	grafana      grafana.Client
	url          string
	apiKey       string
	basicAuth    *url.Userinfo
	disabled     map[string]bool
	upstreamOnce sync.Once
	upstream     *mcpgrafana.GrafanaClient
	bound        map[string]*mcp.Tool
}

func newGFBinder(authorizer authz.Authorizer, client grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo, disabled map[string]bool) (*gfBinder, error) {
	if authorizer == nil {
		return nil, errors.New("authorizer is required")
	}
	if client == nil {
		return nil, errors.New("grafana client is required")
	}
	if grafanaURL == "" {
		return nil, errors.New("grafana URL is required")
	}
	return &gfBinder{authorizer: authorizer, grafana: client, url: grafanaURL, apiKey: apiKey, basicAuth: basicAuth, disabled: disabled, bound: map[string]*mcp.Tool{}}, nil
}

func (b *gfBinder) client() *mcpgrafana.GrafanaClient {
	b.upstreamOnce.Do(func() {
		configuration := mcpgrafana.GrafanaConfig{URL: b.url, APIKey: b.apiKey, BasicAuth: b.basicAuth}
		b.upstream = mcpgrafana.NewGrafanaClient(mcpgrafana.WithGrafanaConfig(context.Background(), configuration), b.url, b.apiKey, b.basicAuth)
	})
	return b.upstream
}

func (b *gfBinder) bindOrgTool(target Registrar, role authz.Role, tool mcpgrafana.Tool) {
	maybeAddTool(target, b.disabled, b.describe(tool, ""), b.wrap(role, "", "", "", tool))
}

func (b *gfBinder) bindDatasourceTool(target Registrar, role authz.Role, tenantType authz.TenantType, datasourceType grafana.DatasourceType, argument string, tool mcpgrafana.Tool) {
	maybeAddTool(target, b.disabled, b.describe(tool, argument), b.wrap(role, tenantType, datasourceType, argument, tool))
}

func (b *gfBinder) bindDatasourceFanoutTool(target Registrar, role authz.Role, tenantType authz.TenantType, argument string, tool mcpgrafana.Tool) {
	maybeAddTool(target, b.disabled, b.describe(tool, ""), b.wrapFanout(role, tenantType, argument, tool))
}

func (b *gfBinder) describe(tool mcpgrafana.Tool, demoteArgument string) *mcp.Tool {
	b.bound[tool.Tool.Name] = tool.Tool
	return withOrg(withSummary(tool.Tool), demoteArgument)
}

func (b *gfBinder) wrapFanout(role authz.Role, tenantType authz.TenantType, argument string, upstream mcpgrafana.Tool) mcp.ToolHandler {
	return func(ctx context.Context, incoming *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		request := *incoming
		organization, refusal := b.requireOrg(ctx, &request, role, tenantType, true)
		if refusal != nil {
			return refusal, nil
		}
		if err := stripOrgArg(&request); err != nil {
			return failure("malformed arguments", err), nil
		}
		ctx = b.attachGrafana(ctx, organization.OrgID)
		uid, err := stringArgument(&request, argument)
		if err != nil {
			return failure("malformed arguments", err), nil
		}
		if uid != "" {
			return invokeUpstream(ctx, upstream, &request)
		}
		datasources, err := b.grafana.ListDatasources(ctx, grafana.RequestOpts{OrgID: organization.OrgID, Caller: authz.GrafanaUser(ctx)})
		if err != nil {
			return failure("list datasources", err), nil
		}
		type entry struct {
			Name  string          `json:"name"`
			UID   string          `json:"uid"`
			Type  string          `json:"type"`
			Rules json.RawMessage `json:"rules,omitempty"`
			Error string          `json:"error,omitempty"`
		}
		entries := make([]entry, 0, len(datasources))
		for _, datasource := range datasources {
			if !datasource.ManageAlerts || !isRulerType(datasource.Type) {
				continue
			}
			subrequest := request
			if err := injectArg(&subrequest, argument, datasource.UID); err != nil {
				entries = append(entries, entry{Name: datasource.Name, UID: datasource.UID, Type: datasource.Type, Error: err.Error()})
				continue
			}
			result, err := invokeUpstream(ctx, upstream, &subrequest)
			switch {
			case err != nil:
				entries = append(entries, entry{Name: datasource.Name, UID: datasource.UID, Type: datasource.Type, Error: err.Error()})
			case result.IsError:
				entries = append(entries, entry{Name: datasource.Name, UID: datasource.UID, Type: datasource.Type, Error: textOf(result)})
			default:
				entries = append(entries, entry{Name: datasource.Name, UID: datasource.UID, Type: datasource.Type, Rules: rulesPayload(result)})
			}
		}
		return jsonResult(map[string]any{"datasources": entries})
	}
}

func isRulerType(pluginType string) bool {
	pluginType = strings.ToLower(pluginType)
	return strings.Contains(pluginType, string(grafana.DSTypePrometheus)) || strings.Contains(pluginType, string(grafana.DSTypeLoki))
}

func textOf(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func rulesPayload(result *mcp.CallToolResult) json.RawMessage {
	text := textOf(result)
	if text == "" {
		return nil
	}
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	encoded, _ := json.Marshal(text)
	return encoded
}

// withOrg copies the complete schema, including definitions and extension
// keywords. Only the organization and datasource-override properties change.
func withOrg(tool *mcp.Tool, demoteArgument string) *mcp.Tool {
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		panic(fmt.Sprintf("tools: tool %q has unmarshalable input schema: %v", tool.Name, err))
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		panic(fmt.Sprintf("tools: tool %q has invalid input schema: %v", tool.Name, err))
	}
	if schema == nil {
		schema = make(map[string]any)
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = make(map[string]any)
	}
	if _, collides := properties["org"]; collides {
		panic(fmt.Sprintf("tools: tool %q already declares an 'org' argument; binder cannot add its own", tool.Name))
	}
	if demoteArgument != "" {
		if current, ok := properties[demoteArgument].(map[string]any); ok {
			current = maps.Clone(current)
			current[jsonSchemaDescriptionKey] = datasourceUIDHint
			properties[demoteArgument] = current
		}
	}
	properties["org"] = map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: orgArgDescription}
	schema["type"], schema["properties"] = jsonTypeObject, properties
	required := []string{"org"}
	if upstreamRequired, ok := schema["required"].([]any); ok {
		for _, value := range upstreamRequired {
			name, ok := value.(string)
			if !ok {
				panic(fmt.Sprintf("tools: tool %q has invalid required properties", tool.Name))
			}
			if name != demoteArgument {
				required = append(required, name)
			}
		}
	}
	schema["required"] = required
	out := *tool
	out.InputSchema = schema
	return &out
}

func (b *gfBinder) wrap(role authz.Role, tenantType authz.TenantType, datasourceType grafana.DatasourceType, argument string, upstream mcpgrafana.Tool) mcp.ToolHandler {
	return func(ctx context.Context, incoming *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		request := *incoming
		organization, refusal := b.requireOrg(ctx, &request, role, tenantType, datasourceType != "")
		if refusal != nil {
			return refusal, nil
		}
		if err := stripOrgArg(&request); err != nil {
			return failure("malformed arguments", err), nil
		}
		ctx = b.attachGrafana(ctx, organization.OrgID)
		if datasourceType == "" {
			return invokeUpstream(ctx, upstream, &request)
		}
		options := grafana.RequestOpts{OrgID: organization.OrgID, Caller: authz.GrafanaUser(ctx)}
		uid, err := stringArgument(&request, argument)
		if err != nil {
			return failure("malformed arguments", err), nil
		}
		if uid != "" {
			datasource, err := b.grafana.LookupDatasourceByUID(ctx, options, uid)
			if err != nil {
				return failure(argument, err), nil
			}
			if !grafana.MatchesType(datasource, datasourceType) {
				return errorResult(fmt.Sprintf("datasource %q is type %q, not compatible with %s tools", uid, datasource.Type, datasourceType)), nil
			}
			return invokeUpstream(ctx, upstream, &request)
		}
		datasources, err := b.grafana.ListDatasources(ctx, options)
		if err != nil {
			return failure("list datasources", err), nil
		}
		matches := grafana.FilterDatasourcesByType(datasources, datasourceType)
		if len(matches) == 0 {
			return errorResult(fmt.Sprintf("org %q has no %s datasource", organization.Name, datasourceType)), nil
		}
		if err := injectArg(&request, argument, matches[0].UID); err != nil {
			return failure("malformed arguments", err), nil
		}
		return invokeUpstream(ctx, upstream, &request)
	}
}

func (b *gfBinder) requireOrg(ctx context.Context, request *mcp.CallToolRequest, role authz.Role, tenantType authz.TenantType, checkTenant bool) (authz.Organization, *mcp.CallToolResult) {
	reference, err := stringArgument(request, "org")
	if err != nil {
		return authz.Organization{}, failure("missing arg", err)
	}
	if reference == "" {
		return authz.Organization{}, errorResult("missing arg: org is required")
	}
	organization, err := b.authorizer.RequireOrg(ctx, reference, role)
	if err != nil {
		return authz.Organization{}, failure("authz", err)
	}
	if checkTenant && tenantType != "" && !organization.HasTenantType(tenantType) {
		return authz.Organization{}, errorResult(fmt.Sprintf("org %q has no tenant of type %q — tool unavailable", reference, tenantType))
	}
	return organization, nil
}

func (b *gfBinder) attachGrafana(ctx context.Context, organizationID int64) context.Context {
	configuration := mcpgrafana.GrafanaConfig{URL: b.url, APIKey: b.apiKey, BasicAuth: b.basicAuth, OrgID: organizationID}
	if subject := authz.GrafanaUser(ctx); subject != "" {
		configuration.ExtraHeaders = map[string]string{"X-Grafana-User": subject}
	}
	ctx = mcpgrafana.WithGrafanaConfig(ctx, configuration)
	return mcpgrafana.WithGrafanaClient(ctx, b.client())
}

func injectArg(request *mcp.CallToolRequest, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode argument: %w", err)
	}
	return editArgs(request, func(arguments map[string]json.RawMessage) { arguments[key] = encoded })
}

func stripOrgArg(request *mcp.CallToolRequest) error {
	return editArgs(request, func(arguments map[string]json.RawMessage) { delete(arguments, "org") })
}

func editArgs(request *mcp.CallToolRequest, edit func(map[string]json.RawMessage)) error {
	arguments := make(map[string]json.RawMessage)
	if err := decodeArguments(request, &arguments); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	if arguments == nil {
		return errors.New("arguments must be an object")
	}
	edit(arguments)
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return fmt.Errorf("encode arguments: %w", err)
	}
	parameters := *request.Params
	parameters.Arguments = encoded
	request.Params = &parameters
	return nil
}

func invokeUpstream(ctx context.Context, upstream mcpgrafana.Tool, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result, err := upstream.Handler(ctx, request)
	if err == nil && result == nil {
		return nil, errors.New("upstream returned no result")
	}
	return result, err
}
