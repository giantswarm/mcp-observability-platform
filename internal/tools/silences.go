// silences.go — Alertmanager v2 silences (local; no upstream
// equivalent in v0.13.0). list_silences resolves the `silencedBy`
// fingerprints that list_alerts surfaces; get_silence returns one
// silence by id.
//
// AM v2 has no server-side state filter on /api/v2/silences — the
// `filter` query param only accepts label matchers. State narrowing
// (active / pending / expired / all) happens here, after the fetch.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// AM v2 silence states. expired ≠ deleted: a silence whose endsAt is
// past stays in the API for retention.window.
const (
	silenceStateActive  = "active"
	silenceStatePending = "pending"
	silenceStateExpired = "expired"
)

func registerSilenceTools(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	registerSilenceListTool(s, az, gc)
	registerSilenceDetailTool(s, az, gc)
}

func registerSilenceListTool(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("list_silences",
			readOnlyAnnotation(),
			mcp.WithDescription("List silences in the org's Alertmanager, paginated and sorted by endsAt asc (soonest expiring first). Each item is minimal (id, state, createdBy, endsAt, matcher count); call get_silence with the id for the full record. Resolves the silencedBy ids that list_alerts returns."),
			orgArg(),
			mcp.WithString("state", mcp.Description("'active' (default) | 'pending' | 'expired' | 'all'")),
			mcp.WithString("matcher", mcp.Description("Alertmanager label matcher to filter silences server-side, e.g. 'alertname=~\"Kube.*\"'.")),
			mcp.WithNumber("page", mcp.Description("0-based page index (default 0)")),
			mcp.WithNumber("pageSize", mcp.Description("Page size (default 50, max 500)")),
			mcp.WithString(datasourceUIDArg, mcp.Description(datasourceUIDHint)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, dsID, err := resolveDatasource(ctx, az, gc, req, authz.RoleViewer, authz.TenantTypeAlerting, grafana.DSTypeAlertmanager)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("authz", err), nil
			}

			pageSize := req.GetInt("pageSize", 0)
			if pageSize <= 0 {
				pageSize = 50
			}
			pageSize = clampInt(pageSize, 1, 500)

			body, err := fetchSilences(ctx, gc, org.OrgID, dsID, req.GetString("matcher", ""))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager proxy failed", err), nil
			}
			result, err := paginateSilences(body, req.GetString("state", ""), req.GetInt("page", 0), pageSize)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse silences", err), nil
			}
			return mcp.NewToolResultJSON(result)
		},
	)
}

func registerSilenceDetailTool(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("get_silence",
			readOnlyAnnotation(),
			mcp.WithDescription("Return the full Alertmanager silence record by id: matchers, startsAt, endsAt, createdBy, comment, state. Use after list_alerts (silencedBy[]) or list_silences."),
			orgArg(),
			mcp.WithString("id", mcp.Required(), mcp.Description("Alertmanager silence id (UUID).")),
			mcp.WithString(datasourceUIDArg, mcp.Description(datasourceUIDHint)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultErrorFromErr("missing arg", err), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, gc, req, authz.RoleViewer, authz.TenantTypeAlerting, grafana.DSTypeAlertmanager)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("authz", err), nil
			}
			path := "alertmanager/api/v2/silence/" + url.PathEscape(id)
			body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, path, nil)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("alertmanager", err), nil
			}
			var silence amSilence
			if err := json.Unmarshal(body, &silence); err != nil {
				return mcp.NewToolResultErrorFromErr("parse silence", err), nil
			}
			return mcp.NewToolResultJSON(silence)
		},
	)
}

// fetchSilences calls /api/v2/silences through the Grafana datasource
// proxy. Only the `filter` query param is server-honoured; state
// narrowing is applied in paginateSilences.
func fetchSilences(ctx context.Context, gc grafana.Client, orgID, dsID int64, matcher string) (json.RawMessage, error) {
	q := url.Values{}
	if matcher != "" {
		// AM expects repeated `filter=...` for multi-matcher; we accept
		// one literal and let the LLM combine matchers via commas if it
		// must (AM parses comma-separated matchers in a single filter).
		q.Set("filter", matcher)
	}
	return gc.DatasourceProxy(ctx, grafanaOpts(ctx, orgID), dsID, "alertmanager/api/v2/silences", q)
}

// amSilence is the subset of AM /api/v2/silences shape we project.
type amSilence struct {
	ID        string         `json:"id"`
	Status    amSilenceState `json:"status"`
	UpdatedAt string         `json:"updatedAt"`
	StartsAt  string         `json:"startsAt"`
	EndsAt    string         `json:"endsAt"`
	CreatedBy string         `json:"createdBy"`
	Comment   string         `json:"comment"`
	Matchers  []amMatcher    `json:"matchers"`
}

type amSilenceState struct {
	State string `json:"state"`
}

type amMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// paginateSilences parses the raw AM response, filters by state, sorts
// by endsAt asc (soonest-expiring first; expired ones float to the
// bottom because their endsAt is in the past — we keep that ordering
// since most callers care about active/pending), and returns a paginated
// minimal projection.
func paginateSilences(raw json.RawMessage, state string, page, pageSize int) (any, error) {
	var silences []amSilence
	if err := json.Unmarshal(raw, &silences); err != nil {
		return nil, fmt.Errorf("unmarshal silences: %w", err)
	}

	if state == "" {
		state = silenceStateActive
	}
	switch state {
	case silenceStateActive, silenceStatePending, silenceStateExpired, filterAll:
	default:
		return nil, fmt.Errorf("state must be one of: active, pending, expired, all")
	}
	if state != filterAll {
		filtered := make([]amSilence, 0, len(silences))
		for _, s := range silences {
			if strings.EqualFold(s.Status.State, state) {
				filtered = append(filtered, s)
			}
		}
		silences = filtered
	}

	sort.Slice(silences, func(i, j int) bool {
		return silences[i].EndsAt < silences[j].EndsAt
	})

	type item struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		CreatedBy string `json:"createdBy,omitempty"`
		EndsAt    string `json:"endsAt"`
		Matchers  int    `json:"matchers"`
		Comment   string `json:"comment,omitempty"`
	}
	start := min(page*pageSize, len(silences))
	end := min(start+pageSize, len(silences))
	items := make([]item, 0, end-start)
	for _, s := range silences[start:end] {
		items = append(items, item{
			ID:        s.ID,
			State:     s.Status.State,
			CreatedBy: s.CreatedBy,
			EndsAt:    s.EndsAt,
			Matchers:  len(s.Matchers),
			Comment:   s.Comment,
		})
	}
	return struct {
		Total    int    `json:"total"`
		Page     int    `json:"page"`
		PageSize int    `json:"pageSize"`
		HasMore  bool   `json:"hasMore"`
		Items    []item `json:"items"`
	}{
		Total:    len(silences),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(silences),
		Items:    items,
	}, nil
}
