package tools

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/identity"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// grafanaOpts packages orgID and caller-subject into a RequestOpts so every
// downstream call attributes to the caller via X-Grafana-User.
func grafanaOpts(ctx context.Context, orgID int64) grafana.RequestOpts {
	return grafana.RequestOpts{OrgID: orgID, Caller: identity.CallerSubject(ctx)}
}

// resolveDatasource runs the three checks every datasource-facing tool needs
// in one shot: the caller must have >= role on org, the org must host the
// required tenant type (empty = skip), and a datasource whose name contains
// all nameContains substrings (case-insensitive) must exist. Errors are
// caller-ready strings so handlers can surface them unchanged.
func resolveDatasource(ctx context.Context, d *Deps, org string, role authz.Role, tenantType obsv1alpha2.TenantType, nameContains ...string) (authz.OrgAccess, int64, error) {
	oa, err := d.Resolver.Require(ctx, identity.CallerAuthz(ctx), org, role)
	if err != nil {
		return authz.OrgAccess{}, 0, err
	}
	if tenantType != "" && !oa.HasTenantType(tenantType) {
		return authz.OrgAccess{}, 0, fmt.Errorf("org %q has no tenant of type %q — tool unavailable", org, tenantType)
	}
	dsID, ok := oa.FindDatasourceID(nameContains...)
	if !ok {
		return authz.OrgAccess{}, 0, fmt.Errorf("no datasource matching %v in org %q", nameContains, org)
	}
	return oa, dsID, nil
}

// datasourceSpec declaratively describes a tool that proxies to a Grafana
// datasource: which datasource to pick, which tenant type is required, and
// which API path/args to use on the downstream.
type datasourceSpec struct {
	Role            authz.Role
	NeedTenant      obsv1alpha2.TenantType
	NameContains    []string
	InstantPath     string
	RangePath       string
	SupportsRange   bool
	ForceRange      bool          // always use RangePath; fill in defaults if start/end missing
	DefaultRangeAgo time.Duration // default start = now - DefaultRangeAgo when ForceRange
	QueryArg        string        // name of the arg on the downstream API that carries the query expression
	ExtraArg        string        // optional pass-through arg (e.g. "limit")
	Timeout         time.Duration // per-tool handler budget; 0 = default 30s
}

// datasourceInvocation bundles the per-call parameters runDatasourceProxy
// needs. Handlers either fill it from req.Params via invocationFromRequest
// (the datasourceProxyHandler default flow) or build one explicitly (the
// dashboards.go run-panel-query and metrics.go histogram-quantile flows
// re-dispatch internally with a synthesised query). Keeping the CallToolRequest
// read-only means the audit middleware captures the actual caller args, not
// a rewritten version — fixes a subtle bug where audit records diverged from
// what the LLM actually sent.
type datasourceInvocation struct {
	Org      string
	Query    string
	Start    string
	End      string
	Step     string
	ExtraInt int // optional, used when spec.ExtraArg is set
}

// invocationFromRequest reads the standard datasource-tool args from req.
func invocationFromRequest(req mcp.CallToolRequest, spec datasourceSpec) datasourceInvocation {
	inv := datasourceInvocation{
		Org:   req.GetString("org", ""),
		Query: req.GetString("query", ""),
		Start: req.GetString("start", ""),
		End:   req.GetString("end", ""),
		Step:  req.GetString("step", ""),
	}
	if spec.ExtraArg != "" {
		inv.ExtraInt = req.GetInt(spec.ExtraArg, 0)
	}
	return inv
}

// datasourceProxyHandler returns a tool handler that: authorises the caller
// on the given org, picks the correct datasource, validates tenant type, and
// forwards the query through Grafana's datasource proxy. The standard flow
// reads its invocation from the request; tools that synthesise an invocation
// (e.g. run_panel_query re-dispatching into query_prometheus) should call
// runDatasourceProxy directly instead of rewriting req.Params.Arguments.
func datasourceProxyHandler(d *Deps, spec datasourceSpec) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, err := req.RequireString("org"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return runDatasourceProxy(ctx, d, spec, invocationFromRequest(req, spec))
	}
}

// runDatasourceProxy runs a datasource-proxy call from an explicit
// invocation — no req.Params reads. Handler sites that re-dispatch
// internally (run_panel_query, query_prometheus_histogram) build the
// invocation themselves and call here; avoids the prior req.Params
// mutation that silently corrupted audit args.
func runDatasourceProxy(ctx context.Context, d *Deps, spec datasourceSpec, inv datasourceInvocation) (*mcp.CallToolResult, error) {
	if inv.Org == "" {
		return mcp.NewToolResultError("missing required argument \"org\""), nil
	}
	oa, dsID, err := resolveDatasource(ctx, d, inv.Org, spec.Role, spec.NeedTenant, spec.NameContains...)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	budget := spec.Timeout
	if budget == 0 {
		budget = 30 * time.Second
	}
	ctx, cancel := withToolTimeout(ctx, budget)
	defer cancel()

	q := url.Values{}
	if spec.QueryArg != "" && inv.Query != "" {
		q.Set(spec.QueryArg, inv.Query)
	}
	start, end := inv.Start, inv.End
	path := spec.InstantPath
	useRange := spec.SupportsRange && (spec.ForceRange || (start != "" && end != ""))
	if useRange {
		path = spec.RangePath
		// Backfill both start + end whenever the spec declares a default
		// range, not only when ForceRange is set. Grafana rejects range
		// queries with a missing end; anchoring both ends here keeps the
		// shape stable regardless of which flag opted the tool in.
		if start == "" && spec.DefaultRangeAgo > 0 {
			start = strconv.FormatInt(time.Now().Add(-spec.DefaultRangeAgo).UnixNano(), 10)
		}
		if end == "" && (spec.ForceRange || spec.DefaultRangeAgo > 0) {
			end = strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		q.Set("start", start)
		q.Set("end", end)
		if inv.Step != "" {
			q.Set("step", inv.Step)
		}
	} else {
		if start != "" {
			q.Set("start", start)
		}
		if end != "" {
			q.Set("end", end)
		}
	}
	if spec.ExtraArg != "" && inv.ExtraInt > 0 {
		q.Set(spec.ExtraArg, strconv.Itoa(inv.ExtraInt))
	}

	observability.GrafanaProxyTotal.WithLabelValues(spec.InstantPath).Inc()
	dsStart := time.Now()
	// Defer the Observe so error paths still record latency — half the
	// signal vanishes during incidents otherwise.
	var proxyErr error
	defer func() {
		status := "ok"
		if proxyErr != nil {
			status = "error"
		}
		observability.GrafanaProxyDuration.WithLabelValues(spec.InstantPath, status).Observe(time.Since(dsStart).Seconds())
	}()
	body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, path, q)
	if err != nil {
		proxyErr = err
		return mcp.NewToolResultErrorFromErr("grafana datasource proxy failed", err), nil
	}
	if capErr := enforceResponseCap(body); capErr != nil {
		return mcp.NewToolResultJSON(capErr)
	}
	return mcp.NewToolResultText(string(body)), nil
}
