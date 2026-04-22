// Package tools — silences.go: alert silences from Alertmanager.
//
// list_silences reads /api/v2/silences (AM live state). A K8s-backed
// list_silence_crs (the silence-operator's declared state) is planned as a
// follow-up — it needs a dynamic client wired through Deps and is its own
// reviewable change.
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

	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func registerSilenceTools(s *mcpsrv.MCPServer, d *Deps) {
	s.AddTool(
		mcp.NewTool("list_silences",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List silences as Alertmanager actually sees them (/api/v2/silences). Use this to answer 'is this alert silenced and by what?'."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("state", mcp.Description("'active' (default) | 'pending' | 'expired' | 'all'.")),
			mcp.WithString("filter", mcp.Description("Alertmanager matcher filter, e.g. 'alertname=~\"Kube.*\"'.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 50, max 500.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			oa, dsID, err := resolveAlertmanagerDS(ctx, d, org)
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
			q := url.Values{}
			if filter := req.GetString("filter", ""); filter != "" {
				q.Set("filter", filter)
			}
			observability.GrafanaProxyTotal.WithLabelValues("alertmanager/api/v2/silences").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, "alertmanager/api/v2/silences", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager silences", err), nil
			}
			result, err := paginateSilences(body, req.GetString("state", ""), page, pageSize)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse silences", err), nil
			}
			return mcp.NewToolResultJSON(result)
		},
	)
}

// amSilence projects Alertmanager's /api/v2/silences shape.
type amSilence struct {
	ID     string `json:"id"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	StartsAt  string `json:"startsAt"`
	EndsAt    string `json:"endsAt"`
	UpdatedAt string `json:"updatedAt"`
	CreatedBy string `json:"createdBy"`
	Comment   string `json:"comment"`
	Matchers  []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		IsEqual bool   `json:"isEqual"`
	} `json:"matchers"`
}

// paginateSilences sorts by EndsAt asc (so soon-to-expire surface first),
// applies the state filter, and returns a minimal projection.
func paginateSilences(raw json.RawMessage, state string, page, pageSize int) (any, error) {
	var sils []amSilence
	if err := json.Unmarshal(raw, &sils); err != nil {
		return nil, fmt.Errorf("unmarshal silences: %w", err)
	}
	if state == "" {
		state = amActive
	}
	if state != filterAll {
		filtered := sils[:0]
		for _, s := range sils {
			if strings.EqualFold(s.Status.State, state) {
				filtered = append(filtered, s)
			}
		}
		sils = filtered
	}
	sort.Slice(sils, func(i, j int) bool { return sils[i].EndsAt < sils[j].EndsAt })

	type item struct {
		ID        string   `json:"id"`
		State     string   `json:"state"`
		StartsAt  string   `json:"startsAt"`
		EndsAt    string   `json:"endsAt"`
		CreatedBy string   `json:"createdBy,omitempty"`
		Comment   string   `json:"comment,omitempty"`
		Matchers  []string `json:"matchers"`
	}
	start := min(page*pageSize, len(sils))
	end := min(start+pageSize, len(sils))
	items := make([]item, 0, end-start)
	for _, s := range sils[start:end] {
		matchers := make([]string, 0, len(s.Matchers))
		for _, m := range s.Matchers {
			op := "="
			switch {
			case m.IsRegex && m.IsEqual:
				op = "=~"
			case m.IsRegex && !m.IsEqual:
				op = "!~"
			case !m.IsRegex && !m.IsEqual:
				op = "!="
			}
			matchers = append(matchers, fmt.Sprintf("%s%s%q", m.Name, op, m.Value))
		}
		items = append(items, item{
			ID: s.ID, State: s.Status.State,
			StartsAt: s.StartsAt, EndsAt: s.EndsAt,
			CreatedBy: s.CreatedBy, Comment: s.Comment, Matchers: matchers,
		})
	}
	return struct {
		Total    int    `json:"total"`
		Page     int    `json:"page"`
		PageSize int    `json:"pageSize"`
		HasMore  bool   `json:"hasMore"`
		Items    []item `json:"items"`
	}{
		Total: len(sils), Page: page, PageSize: pageSize, HasMore: end < len(sils),
		Items: items,
	}, nil
}
