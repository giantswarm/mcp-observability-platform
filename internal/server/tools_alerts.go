package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func registerAlertTools(s *mcpsrv.MCPServer, d *deps) {
	registerAlertDetailTool(s, d)
	registerSilenceTools(s, d)

	s.AddTool(
		mcp.NewTool("list_alerts",
			mcp.WithDescription("List alerts in the org's Alertmanager, paginated and sorted by severity desc. Each item is minimal (name, state, severity, fingerprint); read alertmanager://org/{name}/alert/{fingerprint} for full detail."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("state", mcp.Description("'active' (default) | 'silenced' | 'inhibited' | 'all'")),
			mcp.WithString("filter", mcp.Description("Alertmanager label matcher, e.g. 'alertname=~\"Kube.*\"' or 'severity=\"critical\"'")),
			mcp.WithNumber("page", mcp.Description("0-based page index (default 0)")),
			mcp.WithNumber("pageSize", mcp.Description("Page size (default 50, max 500)")),
		),
		instrument("list_alerts", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := requireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			oa, dsID, err := resolveAlertmanagerDS(ctx, d, org)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()

			page := intArg(args, "page")
			pageSize := intArg(args, "pageSize")
			if pageSize <= 0 {
				pageSize = 50
			}
			pageSize = clampInt(pageSize, 1, 500)

			body, err := fetchAlerts(ctx, d, oa.OrgID, dsID, strArg(args, "state"), strArg(args, "filter"))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager proxy failed", err), nil
			}
			result, err := paginateAlerts(body, page, pageSize)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse alerts", err), nil
			}
			return mcp.NewToolResultJSON(result)
		}),
	)
}

// registerAlertDetailTool exposes a per-alert read tool. Replaces the prior
// alertmanager://org/{name}/alert/{fingerprint} resource (LLMs handle tools
// far more reliably than resources).
func registerAlertDetailTool(s *mcpsrv.MCPServer, d *deps) {
	s.AddTool(
		mcp.NewTool("get_alert",
			mcp.WithDescription("Return the full Alertmanager alert object for a single fingerprint: labels, annotations, timestamps, generatorURL, silencedBy/inhibitedBy. Use after list_alerts."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("fingerprint", mcp.Required(), mcp.Description("Alertmanager fingerprint (from list_alerts.items[].fingerprint).")),
		),
		instrument("get_alert", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := requireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			fp := strArg(args, "fingerprint")
			if fp == "" {
				return mcp.NewToolResultError("missing required argument 'fingerprint'"), nil
			}
			oa, dsID, err := resolveAlertmanagerDS(ctx, d, org)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			body, err := fetchAlerts(ctx, d, oa.OrgID, dsID, "all", "")
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager", err), nil
			}
			alert, err := findAlertByFingerprint(body, fp)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse alerts", err), nil
			}
			if alert == nil {
				return mcp.NewToolResultError(fmt.Sprintf("alert with fingerprint %q not found in org %q", fp, org)), nil
			}
			return mcp.NewToolResultJSON(alert)
		}),
	)
}

// resolveAlertmanagerDS is the alertmanager-specific specialisation of
// resolveDatasource, kept as its own name so call sites in resources.go read
// at the same abstraction level as the tool handlers.
func resolveAlertmanagerDS(ctx context.Context, d *deps, org string) (authz.OrgAccess, int64, error) {
	return resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeAlerting, "alertmanager")
}

// fetchAlerts calls Alertmanager's /api/v2/alerts through the Grafana
// datasource proxy with the requested state/filter narrowing applied
// server-side. Defaults state to "active" when empty.
func fetchAlerts(ctx context.Context, d *deps, orgID, dsID int64, state, filter string) (json.RawMessage, error) {
	if state == "" {
		state = "active"
	}
	q := url.Values{}
	switch state {
	case "active":
		q.Set("active", "true")
		q.Set("silenced", "false")
		q.Set("inhibited", "false")
	case "silenced":
		q.Set("silenced", "true")
		q.Set("active", "false")
		q.Set("inhibited", "false")
	case "inhibited":
		q.Set("inhibited", "true")
		q.Set("active", "false")
		q.Set("silenced", "false")
	case "all":
		// no filter
	default:
		return nil, fmt.Errorf("state must be one of: active, silenced, inhibited, all")
	}
	if filter != "" {
		q.Set("filter", filter)
	}
	observability.GrafanaProxyTotal.WithLabelValues("alertmanager/api/v2/alerts").Inc()
	return d.grafana.DatasourceProxy(ctx, grafanaOpts(ctx, orgID), dsID, "alertmanager/api/v2/alerts", q)
}

// amAlert is the subset of Alertmanager's /api/v2/alerts shape we consume.
type amAlert struct {
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
	UpdatedAt   string            `json:"updatedAt"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Status      struct {
		State       string   `json:"state"`
		SilencedBy  []string `json:"silencedBy"`
		InhibitedBy []string `json:"inhibitedBy"`
	} `json:"status"`
	Fingerprint  string `json:"fingerprint"`
	GeneratorURL string `json:"generatorURL"`
}

// paginateAlerts parses the raw AM response, sorts alerts by severity desc,
// then startsAt asc, and returns a paginated minimal projection.
func paginateAlerts(raw json.RawMessage, page, pageSize int) (any, error) {
	var alerts []amAlert
	if err := json.Unmarshal(raw, &alerts); err != nil {
		return nil, fmt.Errorf("unmarshal alerts: %w", err)
	}
	sort.Slice(alerts, func(i, j int) bool {
		si, sj := severityRank(alerts[i].Labels["severity"]), severityRank(alerts[j].Labels["severity"])
		if si != sj {
			return si > sj
		}
		return alerts[i].StartsAt < alerts[j].StartsAt
	})

	type item struct {
		Name        string `json:"name"`
		State       string `json:"state"`
		Severity    string `json:"severity,omitempty"`
		Fingerprint string `json:"fingerprint"`
	}
	start := min(page*pageSize, len(alerts))
	end := min(start+pageSize, len(alerts))
	items := make([]item, 0, end-start)
	for _, a := range alerts[start:end] {
		name := a.Labels["alertname"]
		if name == "" {
			name = "(unnamed)"
		}
		items = append(items, item{
			Name:        name,
			State:       a.Status.State,
			Severity:    a.Labels["severity"],
			Fingerprint: a.Fingerprint,
		})
	}
	return struct {
		Total    int    `json:"total"`
		Page     int    `json:"page"`
		PageSize int    `json:"pageSize"`
		HasMore  bool   `json:"hasMore"`
		Items    []item `json:"items"`
	}{
		Total:    len(alerts),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(alerts),
		Items:    items,
	}, nil
}

// findAlertByFingerprint returns the full Alertmanager record for the given
// fingerprint, or (nil, nil) if not found. Used by the alert-detail resource.
func findAlertByFingerprint(raw json.RawMessage, fingerprint string) (*amAlert, error) {
	var alerts []amAlert
	if err := json.Unmarshal(raw, &alerts); err != nil {
		return nil, fmt.Errorf("unmarshal alerts: %w", err)
	}
	for i := range alerts {
		if alerts[i].Fingerprint == fingerprint {
			return &alerts[i], nil
		}
	}
	return nil, nil
}

func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical", "page":
		return 4
	case "error", "high":
		return 3
	case "warning", "warn":
		return 2
	case "info", "notice", "low":
		return 1
	default:
		return 0
	}
}
