// Package tools — alerts.go: firing alerts from Alertmanager (list + single-fingerprint detail).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func registerAlertTools(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	registerAlertDetailTool(s, az, gc)

	s.AddTool(
		mcp.NewTool("list_alerts",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List alerts in the org's Alertmanager, paginated and sorted by severity desc. Each item is minimal (name, state, severity, fingerprint); call get_alert with the fingerprint for full detail."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("state", mcp.Description("'active' (default) | 'silenced' | 'inhibited' | 'all'")),
			mcp.WithString("filter", mcp.Description("Alertmanager label matcher, e.g. 'alertname=~\"Kube.*\"' or 'severity=\"critical\"'")),
			mcp.WithNumber("page", mcp.Description("0-based page index (default 0)")),
			mcp.WithNumber("pageSize", mcp.Description("Page size (default 50, max 500)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeAlerting, "alertmanager")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()

			page := req.GetInt("page", 0)
			pageSize := req.GetInt("pageSize", 0)
			if pageSize <= 0 {
				pageSize = 50
			}
			pageSize = clampInt(pageSize, 1, 500)

			body, err := fetchAlerts(ctx, gc, org.OrgID, dsID, req.GetString("state", ""), req.GetString("filter", ""))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager proxy failed", err), nil
			}
			result, err := paginateAlerts(body, page, pageSize)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse alerts", err), nil
			}
			return mcp.NewToolResultJSON(result)
		},
	)
}

// registerAlertDetailTool exposes a per-alert read tool. Replaces the prior
// alertmanager://org/{name}/alert/{fingerprint} resource (LLMs handle tools
// far more reliably than resources).
func registerAlertDetailTool(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("get_alert",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return the full Alertmanager alert object for a single fingerprint: labels, annotations, timestamps, generatorURL, silencedBy/inhibitedBy. Use after list_alerts."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("fingerprint", mcp.Required(), mcp.Description("Alertmanager fingerprint (from list_alerts.items[].fingerprint).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			fp, err := req.RequireString("fingerprint")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeAlerting, "alertmanager")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			body, err := fetchAlerts(ctx, gc, org.OrgID, dsID, filterAll, "")
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager", err), nil
			}
			alert, err := findAlertByFingerprint(body, fp)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse alerts", err), nil
			}
			if alert == nil {
				return mcp.NewToolResultError(fmt.Sprintf("alert with fingerprint %q not found in org %q", fp, orgRef)), nil
			}
			return mcp.NewToolResultJSON(alert)
		},
	)
}

// fetchAlerts calls Alertmanager's /api/v2/alerts through the Grafana
// datasource proxy with the requested state/filter narrowing applied
// server-side. Defaults state to "active" when empty.
func fetchAlerts(ctx context.Context, gc grafana.Client, orgID, dsID int64, state, filter string) (json.RawMessage, error) {
	if state == "" {
		state = amActive
	}
	q := url.Values{}
	switch state {
	case amActive:
		q.Set(amActive, "true")
		q.Set("silenced", "false")
		q.Set("inhibited", "false")
	case "silenced":
		q.Set("silenced", "true")
		q.Set(amActive, "false")
		q.Set("inhibited", "false")
	case "inhibited":
		q.Set("inhibited", "true")
		q.Set(amActive, "false")
		q.Set("silenced", "false")
	case filterAll:
		// no filter
	default:
		return nil, fmt.Errorf("state must be one of: active, silenced, inhibited, all")
	}
	if filter != "" {
		q.Set("filter", filter)
	}
	observability.GrafanaProxyTotal.WithLabelValues("alertmanager/api/v2/alerts").Inc()
	return gc.DatasourceProxy(ctx, grafanaOpts(ctx, orgID), dsID, "alertmanager/api/v2/alerts", q)
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
