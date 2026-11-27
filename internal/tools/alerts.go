// alerts.go — Alertmanager v2 firing-alert tools (local; upstream
// covers OnCall, not Alertmanager). list_alerts paginates; get_alert
// fetches one fingerprint.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	tooldesc "github.com/giantswarm/mcp-toolkit/tooldesc/gosdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const (
	severityCritical = "critical"
	severityWarning  = "warning"
	severityInfo     = "info"
)

func registerAlertTools(s Registrar, disabled map[string]bool, az authz.Authorizer, gc grafana.Client) {
	registerAlertDetailTool(s, disabled, az, gc)

	maybeAddTool(s, disabled,
		&mcp.Tool{Name: "list_alerts", Annotations: readOnlyAnnotation(), Description: tooldesc.Describe("List the org's Alertmanager alerts, most severe first, paged; get_alert has one alert's full detail.", ""),
			InputSchema: localInputSchema(map[string]any{
				"state":          map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: "'active' (default) | 'silenced' | 'inhibited' | 'all'"},
				"filter":         map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: "Alertmanager label matcher, e.g. 'alertname=~\"Kube.*\"' or 'severity=\"critical\"'"},
				pageArgument:     map[string]any{jsonSchemaTypeKey: jsonTypeNumber, jsonSchemaDescriptionKey: "0-based page index (default 0)"},
				"pageSize":       map[string]any{jsonSchemaTypeKey: jsonTypeNumber, jsonSchemaDescriptionKey: "Page size (default 50, max 500)"},
				datasourceUIDArg: map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: datasourceUIDHint},
			}),
		},
		func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var arguments struct {
				Org           string  `json:"org"`
				DatasourceUID string  `json:"datasourceUid"`
				State         string  `json:"state"`
				Filter        string  `json:"filter"`
				Page          float64 `json:"page"`
				PageSize      float64 `json:"pageSize"`
			}
			if err := decodeArguments(request, &arguments); err != nil {
				return failure("malformed arguments", err), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, gc, arguments.Org, arguments.DatasourceUID, authz.RoleViewer, authz.TenantTypeAlerting, grafana.DSTypeAlertmanager)
			if err != nil {
				return failure("authz", err), nil
			}

			pageSize := int(arguments.PageSize)
			if pageSize <= 0 {
				pageSize = 50
			}
			pageSize = clampInt(pageSize, 1, 500)

			body, err := fetchAlerts(ctx, gc, org.OrgID, dsID, arguments.State, arguments.Filter)
			if err != nil {
				return failure("alertmanager proxy failed", err), nil
			}
			result, err := paginateAlerts(body, int(arguments.Page), pageSize)
			if err != nil {
				return failure("parse alerts", err), nil
			}
			return jsonResult(result)
		},
	)
}

// registerAlertDetailTool exposes a per-fingerprint read after list_alerts.
// Tool (not a resource) because LLM clients handle tool calls more reliably
// than resource URI templates.
func registerAlertDetailTool(s Registrar, disabled map[string]bool, az authz.Authorizer, gc grafana.Client) {
	maybeAddTool(s, disabled,
		&mcp.Tool{Name: "get_alert", Annotations: readOnlyAnnotation(), Description: tooldesc.Describe("Get one Alertmanager alert by its fingerprint from list_alerts: labels, annotations, times, silencedBy, inhibitedBy.", ""),
			InputSchema: localInputSchema(map[string]any{
				"fingerprint":    map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: "Alertmanager fingerprint (from list_alerts.items[].fingerprint)."},
				datasourceUIDArg: map[string]any{jsonSchemaTypeKey: jsonTypeString, jsonSchemaDescriptionKey: datasourceUIDHint},
			}, "fingerprint"),
		},
		func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var arguments struct {
				Org           string `json:"org"`
				DatasourceUID string `json:"datasourceUid"`
				Fingerprint   string `json:"fingerprint"`
			}
			if err := decodeArguments(request, &arguments); err != nil {
				return failure("malformed arguments", err), nil
			}
			if arguments.Fingerprint == "" {
				return errorResult("missing arg: fingerprint is required"), nil
			}
			fp := arguments.Fingerprint
			org, dsID, err := resolveDatasource(ctx, az, gc, arguments.Org, arguments.DatasourceUID, authz.RoleViewer, authz.TenantTypeAlerting, grafana.DSTypeAlertmanager)
			if err != nil {
				return failure("authz", err), nil
			}
			// AM v2 has no fingerprint matcher; the only round-trip is a full
			// scan with state=all. Acceptable here — the caller already paged
			// list_alerts for this fingerprint, so a re-fetch is bounded.
			body, err := fetchAlerts(ctx, gc, org.OrgID, dsID, filterAll, "")
			if err != nil {
				return failure("alertmanager", err), nil
			}
			alert, err := findAlertByFingerprint(body, fp)
			if err != nil {
				return failure("parse alerts", err), nil
			}
			if alert == nil {
				return errorResult(fmt.Sprintf("alert with fingerprint %q not found", fp)), nil
			}
			return jsonResult(alert)
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
	// Out-of-range pages clamp to the empty tail rather than erroring —
	// LLMs guess pageSize and we'd rather return zero items than confuse
	// them with a hard error.
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
// fingerprint, or (nil, nil) if not found. Used by get_alert.
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
	case severityCritical, pageArgument:
		return 4
	case "error", "high":
		return 3
	case severityWarning, "warn":
		return 2
	case severityInfo, "notice", "low":
		return 1
	default:
		return 0
	}
}
