package tools

import (
	"sync"
	"testing"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const toolsDocHeader = "# The tools\n\nGenerated from the registered tools (`make tools-doc`), none disabled by `--disabled-tools`. " +
	"The descriptions and schemas of the tools delegated to grafana/mcp-grafana are upstream's, led by a summary line of ours where upstream's first line is longer than muster shows; " +
	"the binder adds the `org` argument and replaces the `datasourceUid` description."

// localTools are the tools this server implements itself. They are held to
// the whole budget; the delegated tools only to the summary line, since the
// rest of their text is upstream's.
var localTools = []string{"get_alert", "get_silence", "list_alerts", "list_orgs", "list_silences"}

var docsServer = sync.OnceValue(func() *mcpsrv.MCPServer {
	gc, err := grafana.New(grafana.Config{URL: "http://grafana.invalid", Token: "docs"})
	if err != nil {
		panic(err)
	}
	s := mcpsrv.NewMCPServer("mcp-observability-platform", "docs", mcpsrv.WithToolCapabilities(true))
	if err := RegisterAll(s, &authztest.Fake{}, gc, "http://grafana.invalid", "docs", nil, nil); err != nil {
		panic(err)
	}
	return s
})

func registeredTools(t *testing.T) map[string]mcp.Tool {
	t.Helper()
	out := map[string]mcp.Tool{}
	for name, st := range docsServer().ListTools() {
		out[name] = st.Tool
	}
	return out
}

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, docsServer(), toolsDocHeader, "../../docs/tools.md")
}

// TestToolDescriptionBudgets holds the descriptions to the budget of
// giantswarm/giantswarm#38592: every summary line, and the local tools'
// whole text.
func TestToolDescriptionBudgets(t *testing.T) {
	all := registeredTools(t)
	var local, every []mcp.Tool
	for _, name := range localTools {
		tool, ok := all[name]
		if !ok {
			t.Fatalf("local tool %s is not registered", name)
		}
		local = append(local, tool)
	}
	for _, tool := range all {
		every = append(every, tool)
	}
	if err := tooldesc.Check(local, tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
	if err := tooldesc.Check(every, tooldesc.Budget{Prefix: tooldesc.DefaultBudget.Prefix}); err != nil {
		t.Error(err)
	}
}

// TestUpstreamSummariesNameRegisteredTools: a summary for a tool upstream
// renamed or this server stopped binding would silently do nothing.
func TestUpstreamSummariesNameRegisteredTools(t *testing.T) {
	all := registeredTools(t)
	for name := range upstreamSummaries {
		if _, ok := all[name]; !ok {
			t.Errorf("upstreamSummaries names %s, which is not registered", name)
		}
	}
}

// TestReadOnlyToolsAreNotDestructive: a read-only tool says so in full, or
// mcp-go's defaults report it destructive and not idempotent.
func TestReadOnlyToolsAreNotDestructive(t *testing.T) {
	for name, tool := range registeredTools(t) {
		a := tool.Annotations
		if a.ReadOnlyHint == nil || !*a.ReadOnlyHint {
			continue
		}
		if a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s is read-only but reported destructive", name)
		}
		if a.IdempotentHint == nil || !*a.IdempotentHint {
			t.Errorf("%s is read-only but not reported idempotent", name)
		}
	}
}
