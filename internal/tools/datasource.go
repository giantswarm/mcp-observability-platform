// Package tools — datasource.go: shared datasource-proxy dispatcher + spec for Mimir/Loki/Tempo/Alertmanager tools.
package tools

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// grafanaOpts packages orgID and caller-subject into a RequestOpts so every
// downstream call attributes to the caller via X-Grafana-User.
func grafanaOpts(ctx context.Context, orgID int64) grafana.RequestOpts {
	return grafana.RequestOpts{OrgID: orgID, Caller: authz.CallerSubject(ctx)}
}

// resolveDatasource runs the three checks every datasource-facing tool needs
// in one shot: the caller must have >= role on org, the org must host the
// required tenant type (empty = skip), and a datasource whose name contains
// all nameContains substrings (case-insensitive) must exist. Errors are
// caller-ready strings so handlers can surface them unchanged.
func resolveDatasource(ctx context.Context, d *Deps, orgRef string, role authz.Role, tenantType authz.TenantType, nameContains ...string) (authz.Organization, int64, error) {
	org, err := d.Resolver.Require(ctx, authz.CallerFromContext(ctx), orgRef, role)
	if err != nil {
		return authz.Organization{}, 0, err
	}
	if tenantType != "" && !org.HasTenantType(tenantType) {
		return authz.Organization{}, 0, fmt.Errorf("org %q has no tenant of type %q — tool unavailable", orgRef, tenantType)
	}
	dsID, ok := org.FindDatasourceID(nameContains...)
	if !ok {
		return authz.Organization{}, 0, fmt.Errorf("no datasource matching %v in org %q", nameContains, orgRef)
	}
	return org, dsID, nil
}

// datasourceSpec declaratively describes a tool that proxies to a Grafana
// datasource: which datasource to pick, which tenant type is required, and
// which API path/args to use on the downstream.
type datasourceSpec struct {
	Role          authz.Role
	NeedTenant    authz.TenantType
	NameContains  []string
	InstantPath   string
	RangePath     string
	SupportsRange bool
	QueryArg      string        // arg on the downstream API that carries the query expression
	ExtraArg      string        // optional pass-through arg (e.g. "limit")
	Timeout       time.Duration // per-tool handler budget; 0 = default 30s
}

// datasourceInvocation bundles the per-call parameters runDatasourceProxy
// needs. Handlers either read them from the request (the datasourceProxyHandler
// default flow) or synthesise them (the dashboards.go run-panel-query and
// metrics.go histogram-quantile flows, which re-dispatch internally with a
// generated query). Keeping the CallToolRequest read-only means the audit
// middleware records the caller's actual args, not a rewritten version.
type datasourceInvocation struct {
	Org      string
	Query    string
	Start    string
	End      string
	Step     string
	ExtraInt int // optional, used when spec.ExtraArg is set
}

// datasourceProxyHandler returns a tool handler that: authorises the caller
// on the given org, picks the correct datasource, validates tenant type, and
// forwards the query through Grafana's datasource proxy. The standard flow
// reads its invocation from the request; tools that synthesise an invocation
// (run_panel_query, query_prometheus_histogram) call runDatasourceProxy
// directly with their own datasourceInvocation.
func datasourceProxyHandler(d *Deps, spec datasourceSpec) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, err := req.RequireString("org"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
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
		return runDatasourceProxy(ctx, d, spec, inv)
	}
}

// runDatasourceProxy runs a datasource-proxy call from an explicit
// invocation — no req.Params reads. Handler sites that re-dispatch
// internally (run_panel_query, query_prometheus_histogram) build the
// invocation themselves and call here; avoids the prior req.Params
// mutation that silently corrupted audit args.
//
// Per-datasource defaulting (e.g. Loki needs start+end and the caller can
// default to last-hour) is the caller's responsibility — see dashboards.go
// run_panel_query Loki branch. Keeping defaulting out of here means this
// function's only conditional is "instant vs range path".
func runDatasourceProxy(ctx context.Context, d *Deps, spec datasourceSpec, inv datasourceInvocation) (*mcp.CallToolResult, error) {
	if inv.Org == "" {
		return mcp.NewToolResultError("missing required argument \"org\""), nil
	}
	org, dsID, err := resolveDatasource(ctx, d, inv.Org, spec.Role, spec.NeedTenant, spec.NameContains...)
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
	// Use the range path when the spec opts in and both start+end are
	// supplied — callers that want "always use range" default their own
	// start/end before calling.
	path := spec.InstantPath
	useRange := spec.SupportsRange && inv.Start != "" && inv.End != ""
	if useRange {
		path = spec.RangePath
		q.Set("start", inv.Start)
		q.Set("end", inv.End)
		if inv.Step != "" {
			q.Set("step", inv.Step)
		}
	} else {
		if inv.Start != "" {
			q.Set("start", inv.Start)
		}
		if inv.End != "" {
			q.Set("end", inv.End)
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
	body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, path, q)
	if err != nil {
		proxyErr = err
		return mcp.NewToolResultErrorFromErr("grafana datasource proxy failed", err), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}
