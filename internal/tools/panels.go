// Package tools — panels.go: panel-image rendering via Grafana's /render endpoint.
package tools

import (
	"cmp"
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// maxRenderedImageBytes caps the PNG payload returned by get_panel_image.
// Large renders (2000x1000 with dense data) easily hit multi-MB sizes and
// will blow past most LLM context windows even as base64. 4 MiB is a
// reasonable upper bound for practical panels.
const maxRenderedImageBytes = 4 * 1024 * 1024

func registerPanelTools(s *mcpsrv.MCPServer, d *Deps) {
	s.AddTool(
		mcp.NewTool("get_panel_image",
			ReadOnlyAnnotation(),
			mcp.WithDescription(
				"Render a Grafana dashboard panel as a PNG image and return it as an MCP image resource. "+
					"Requires the 'grafana-image-renderer' plugin, or the standalone renderer service "+
					"(grafana/grafana-image-renderer) wired to Grafana via GF_RENDERING_SERVER_URL + "+
					"GF_RENDERING_CALLBACK_URL. Without the renderer, Grafana returns an HTML error and "+
					"this tool returns an actionable error message."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Required(), mcp.Description("Panel ID (from get_dashboard_summary or get_dashboard_panel_queries).")),
			mcp.WithString("from", mcp.Description("Grafana time (e.g. 'now-1h', unix ms, RFC3339). Default 'now-1h'.")),
			mcp.WithString("to", mcp.Description("Grafana time. Default 'now'.")),
			mcp.WithNumber("width", mcp.Description("Image width in px (default 1000).")),
			mcp.WithNumber("height", mcp.Description("Image height in px (default 500).")),
			mcp.WithString("theme", mcp.Description("'light' | 'dark' (default 'light').")),
			mcp.WithString("tz", mcp.Description("IANA timezone for time axis, e.g. 'Europe/Paris'. Default: UTC.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			panelID := req.GetInt("panelId", 0)
			if panelID <= 0 {
				return mcp.NewToolResultError("missing required argument 'panelId'"), nil
			}
			org, err := d.Authorizer.Require(ctx, authz.CallerFromContext(ctx), orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Rendering is CPU-heavy on the renderer service; give it room.
			ctx, cancel := withToolTimeout(ctx, 45*time.Second)
			defer cancel()

			// Short-circuit with a structured error when the plugin isn't
			// installed — without this, Grafana returns a PNG of its own
			// error message and the tool appears to succeed.
			if present, err := d.Grafana.HasImageRenderer(ctx); err == nil && !present {
				return mcp.NewToolResultJSON(struct {
					Error string `json:"error"`
					Hint  string `json:"hint"`
					Docs  string `json:"docs"`
				}{
					Error: "image_renderer_not_installed",
					Hint:  "ask your Grafana administrator to install the grafana-image-renderer plugin, or deploy the renderer service (grafana/grafana-image-renderer) and set GF_RENDERING_SERVER_URL + GF_RENDERING_CALLBACK_URL on Grafana",
					Docs:  "https://grafana.com/grafana/plugins/grafana-image-renderer/",
				})
			}

			q := url.Values{}
			q.Set("from", cmp.Or(req.GetString("from", ""), "now-1h"))
			q.Set("to", cmp.Or(req.GetString("to", ""), "now"))
			width := req.GetInt("width", 0)
			if width <= 0 {
				width = 1000
			}
			height := req.GetInt("height", 0)
			if height <= 0 {
				height = 500
			}
			q.Set("width", strconv.Itoa(width))
			q.Set("height", strconv.Itoa(height))
			if theme := req.GetString("theme", ""); theme != "" {
				q.Set("theme", theme)
			}
			if tz := req.GetString("tz", ""); tz != "" {
				q.Set("tz", tz)
			}
			q.Set("orgId", strconv.FormatInt(org.OrgID, 10))

			png, contentType, err := d.Grafana.RenderPanel(ctx, grafanaOpts(ctx, org.OrgID), uid, panelID, q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("render panel", err), nil
			}
			if len(png) > maxRenderedImageBytes {
				return mcp.NewToolResultJSON(struct {
					Error string `json:"error"`
					Bytes int    `json:"bytes"`
					Limit int    `json:"limit"`
					Hint  string `json:"hint"`
				}{
					Error: "image_too_large",
					Bytes: len(png),
					Limit: maxRenderedImageBytes,
					Hint:  "reduce width/height, narrow the time range, or use get_dashboard_panel_queries + query_prometheus to summarise numerically",
				})
			}
			// MCP ImageContent uses base64-encoded data.
			return mcp.NewToolResultImage(
				"panel render: dashboard "+uid+" panel "+strconv.Itoa(panelID),
				base64.StdEncoding.EncodeToString(png),
				contentType,
			), nil
		},
	)
}
