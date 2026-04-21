package middleware

import (
	"context"
	"fmt"
	"net/url"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// ResolveDatasource runs the three checks every datasource-facing tool needs
// in one shot: the caller must have >= role on org, the org must host the
// required tenant type (empty = skip), and a datasource whose name contains
// all nameContains substrings (case-insensitive) must exist. Errors are
// caller-ready strings so handlers can surface them unchanged.
func ResolveDatasource(ctx context.Context, d *Deps, org string, role authz.Role, tenantType obsv1alpha2.TenantType, nameContains ...string) (authz.OrgAccess, int64, error) {
	oa, err := d.Resolver.Require(ctx, CallerAuthz(ctx), org, role)
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

// DatasourceSpec declaratively describes a tool that proxies to a Grafana
// datasource: which datasource to pick, which tenant type is required, and
// which API path/args to use on the downstream.
type DatasourceSpec struct {
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

// DatasourceProxyHandler returns a tool handler that: authorises the caller
// on the given org, picks the correct datasource, validates tenant type, and
// forwards the query through Grafana's datasource proxy.
func DatasourceProxyHandler(d *Deps, spec DatasourceSpec) Handler {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		org, errRes := RequireOrg(args)
		if errRes != nil {
			return errRes, nil
		}
		oa, dsID, err := ResolveDatasource(ctx, d, org, spec.Role, spec.NeedTenant, spec.NameContains...)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		budget := spec.Timeout
		if budget == 0 {
			budget = 30 * time.Second
		}
		ctx, cancel := WithToolTimeout(ctx, budget)
		defer cancel()

		q := url.Values{}
		if spec.QueryArg != "" {
			if v := StrArg(args, "query"); v != "" {
				q.Set(spec.QueryArg, v)
			}
		}
		start := StrArg(args, "start")
		end := StrArg(args, "end")
		path := spec.InstantPath
		useRange := spec.SupportsRange && (spec.ForceRange || (start != "" && end != ""))
		if useRange {
			path = spec.RangePath
			// Backfill both start + end whenever the spec declares a default
			// range, not only when ForceRange is set. Grafana rejects range
			// queries with a missing end; anchoring both ends here keeps the
			// shape stable regardless of which flag opted the tool in.
			if start == "" && spec.DefaultRangeAgo > 0 {
				start = fmt.Sprintf("%d", time.Now().Add(-spec.DefaultRangeAgo).UnixNano())
			}
			if end == "" && (spec.ForceRange || spec.DefaultRangeAgo > 0) {
				end = fmt.Sprintf("%d", time.Now().UnixNano())
			}
			q.Set("start", start)
			q.Set("end", end)
			if step := StrArg(args, "step"); step != "" {
				q.Set("step", step)
			}
		} else {
			if start != "" {
				q.Set("start", start)
			}
			if end != "" {
				q.Set("end", end)
			}
		}
		if spec.ExtraArg != "" {
			if v := IntArg(args, spec.ExtraArg); v > 0 {
				q.Set(spec.ExtraArg, fmt.Sprintf("%d", v))
			}
		}

		observability.GrafanaProxyTotal.WithLabelValues(spec.InstantPath).Inc()
		dsStart := time.Now()
		body, err := d.Grafana.DatasourceProxy(ctx, GrafanaOpts(ctx, oa.OrgID), dsID, path, q)
		observability.GrafanaProxyDuration.WithLabelValues(spec.InstantPath).Observe(time.Since(dsStart).Seconds())
		if err != nil {
			return mcp.NewToolResultErrorFromErr("grafana datasource proxy failed", err), nil
		}
		if capErr := EnforceResponseCap(body); capErr != nil {
			return mcp.NewToolResultJSON(capErr)
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}
