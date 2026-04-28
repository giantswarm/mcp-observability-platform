// rules.go — list_loki_rules: alerting + recording rules from the org's
// Loki ruler. Local because upstream's alerting_manage_rules drops
// recording rules at projection time (mcp-grafana
// alerting_manage_rules_datasource.go convertPrometheusRulesToSummary
// switches v1.RecordingRule → continue), so getting recording rules
// requires hitting the ruler endpoint directly.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func registerLokiRulesTool(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("list_loki_rules",
			readOnlyAnnotation(),
			mcp.WithDescription("List alerting and recording rules from the org's Loki ruler. Returns both rule types in a flat list keyed by type='alerting'|'recording'. Use the 'type' filter to narrow."),
			orgArg(),
			mcp.WithString("type", mcp.Description("'alerting' | 'recording' | 'all' (default 'all').")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, dsID, err := resolveDatasource(ctx, az, req, authz.RoleViewer, authz.TenantTypeData, grafana.DSKindLoki)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("authz", err), nil
			}
			body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "prometheus/api/v1/rules", nil)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("loki rules", err), nil
			}
			result, err := projectLokiRules(body, req.GetString("type", filterAll))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse loki rules", err), nil
			}
			return mcp.NewToolResultJSON(result)
		},
	)
}

// rulesEnvelope is the Prometheus-shaped /rules response Loki's ruler emits.
// Loki's "alerting" and "recording" rules share enough fields that one
// flat struct is enough; recording-only fields stay zero on alerting rules
// and vice versa.
type rulesEnvelope struct {
	Data struct {
		Groups []struct {
			Name  string `json:"name"`
			File  string `json:"file"`
			Rules []rule `json:"rules"`
		} `json:"groups"`
	} `json:"data"`
}

type rule struct {
	Type           string            `json:"type"` // "alerting" | "recording"
	Name           string            `json:"name"`
	Query          string            `json:"query"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
	State          string            `json:"state,omitempty"`
	Health         string            `json:"health,omitempty"`
	LastError      string            `json:"lastError,omitempty"`
	LastEvaluation string            `json:"lastEvaluation,omitempty"`
}

type ruleItem struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Group       string            `json:"group"`
	Query       string            `json:"query"`
	State       string            `json:"state,omitempty"`
	Health      string            `json:"health,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// projectLokiRules flattens Loki's grouped rules response and filters by
// type. typeFilter accepts "alerting", "recording", "all", or "" (≡ "all").
func projectLokiRules(raw json.RawMessage, typeFilter string) (any, error) {
	var env rulesEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("unmarshal rules: %w", err)
	}
	keep := func(string) bool { return true }
	switch typeFilter {
	case "", filterAll:
		// keep all
	case "alerting", "recording":
		want := typeFilter
		keep = func(t string) bool { return t == want }
	default:
		return nil, fmt.Errorf("type must be one of: alerting, recording, all")
	}

	var items []ruleItem
	for _, g := range env.Data.Groups {
		for _, r := range g.Rules {
			if !keep(r.Type) {
				continue
			}
			items = append(items, ruleItem{
				Type:        r.Type,
				Name:        r.Name,
				Group:       g.Name,
				Query:       r.Query,
				State:       r.State,
				Health:      r.Health,
				Labels:      r.Labels,
				Annotations: r.Annotations,
			})
		}
	}
	return struct {
		Total int        `json:"total"`
		Items []ruleItem `json:"items"`
	}{Total: len(items), Items: items}, nil
}
