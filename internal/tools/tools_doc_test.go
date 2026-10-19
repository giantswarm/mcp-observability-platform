package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const toolsDocHeader = "# The tools\n\nGenerated from the registered tools (`make tools-doc`), none disabled by `--disabled-tools`. " +
	"The descriptions and schemas of the tools delegated to grafana/mcp-grafana are upstream's, led by a summary line of ours where upstream's first line is longer than muster shows; " +
	"the binder adds the `org` argument and, on the datasource-bound tools, replaces the `datasourceUid` description."

type docs struct {
	server *mcpsrv.MCPServer
	binder *gfBinder
}

var docsServer = sync.OnceValue(func() docs {
	gc, err := grafana.New(grafana.Config{URL: "http://grafana.invalid", Token: "docs"})
	if err != nil {
		panic(err)
	}
	az := &authztest.Fake{}
	b, err := newGFBinder(az, gc, "http://grafana.invalid", "docs", nil, nil)
	if err != nil {
		panic(err)
	}
	s := mcpsrv.NewMCPServer("mcp-observability-platform", "docs", mcpsrv.WithToolCapabilities(true))
	registerAll(s, az, gc, b, nil)
	return docs{server: s, binder: b}
})

func registeredTools() map[string]mcp.Tool {
	out := map[string]mcp.Tool{}
	for name, st := range docsServer().server.ListTools() {
		out[name] = st.Tool
	}
	return out
}

// upstreamHash is a short hash of an upstream tool's description and input
// schema, as upstreamSummaries pins them.
func upstreamHash(t mcp.Tool) string {
	sum := sha256.Sum256([]byte(t.Description + "\n" + string(t.RawInputSchema)))
	return hex.EncodeToString(sum[:6])
}

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, docsServer().server, toolsDocHeader, "../../docs/tools.md")
}

// TestToolDescriptionBudgets holds the descriptions to the budget of
// giantswarm/giantswarm#38592: every summary line, and the whole text of
// the tools this server implements itself. A delegated tool's text past
// the summary is upstream's.
func TestToolDescriptionBudgets(t *testing.T) {
	var local, every []mcp.Tool
	for name, tool := range registeredTools() {
		every = append(every, tool)
		if _, delegated := docsServer().binder.bound[name]; !delegated {
			local = append(local, tool)
		}
	}
	if len(local) == 0 {
		t.Fatal("no local tools registered")
	}
	if err := tooldesc.Check(local, tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
	if err := tooldesc.Check(every, tooldesc.Budget{Prefix: tooldesc.DefaultBudget.Prefix}); err != nil {
		t.Error(err)
	}
}

// TestUpstreamSummariesAreCurrent: a summary describes the upstream text it
// was written against. When mcp-grafana changes a summarised tool, someone
// has to check the summary; when upstream's own first line fits, ours goes.
func TestUpstreamSummariesAreCurrent(t *testing.T) {
	bound := docsServer().binder.bound
	for name, us := range upstreamSummaries {
		up, ok := bound[name]
		if !ok {
			t.Errorf("summaries.go has a summary for %s, which is not bound", name)
			continue
		}
		first, _, _ := strings.Cut(up.Description, "\n")
		if n := utf8.RuneCountInString(strings.TrimSpace(first)); n <= tooldesc.SummaryMax {
			t.Errorf("upstream's first line of %s fits now (%d runes): drop its entry in summaries.go", name, n)
		}
		if got := upstreamHash(up); got != us.upstream {
			t.Errorf("mcp-grafana changed %s: re-read its handler, check its summary in summaries.go, then set its hash to %q", name, got)
		}
	}
}

// TestReadOnlyToolsAreNotDestructive: a read-only tool says so in full, or
// mcp-go's defaults report it destructive and not idempotent.
func TestReadOnlyToolsAreNotDestructive(t *testing.T) {
	for name, tool := range registeredTools() {
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
