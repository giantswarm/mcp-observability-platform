// Package server — prompts.go registers MCP prompts (named playbooks the
// LLM can invoke with structured arguments). Prompts are distinct from tools:
// tools do work, prompts produce a prepared conversation turn.
package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func registerPrompts(s *mcpsrv.MCPServer, _ *deps) {
	s.AddPrompt(
		mcp.NewPrompt("investigate-alert",
			mcp.WithPromptDescription(
				"Investigate a specific Alertmanager alert: pulls its full detail and guides the LLM "+
					"to correlate it with recent logs, metrics, and firing rules, then produce a short "+
					"triage report. Requires the alert's org and fingerprint from list_alerts."),
			mcp.WithArgument("org",
				mcp.ArgumentDescription("Organization — Grafana displayName or CR name. See list_orgs."),
				mcp.RequiredArgument(),
			),
			mcp.WithArgument("fingerprint",
				mcp.ArgumentDescription("Alertmanager alert fingerprint (from list_alerts.items[].fingerprint)."),
				mcp.RequiredArgument(),
			),
			mcp.WithArgument("lookback",
				mcp.ArgumentDescription("Time window for correlated queries (default: 1h)."),
			),
		),
		handleInvestigateAlert,
	)
}

func handleInvestigateAlert(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments
	org := args["org"]
	fp := args["fingerprint"]
	lookback := args["lookback"]
	if lookback == "" {
		lookback = "1h"
	}
	if org == "" || fp == "" {
		return nil, fmt.Errorf("org and fingerprint are required")
	}

	system := `You are an observability SRE assistant. Investigate a firing alert and produce a short triage report. Use only the MCP tools in this server; do NOT invent data. Prefer discovery tools (list_*_label_names, list_*_label_values) before writing queries.

Follow this sequence, and STOP as soon as you have enough to report:

1. Read the alert detail:
   - call get_alert(org="` + org + `", fingerprint="` + fp + `")
   - Extract: alertname, severity, labels (especially 'cluster', 'namespace', 'job', 'pod', 'service'), annotations (summary, description, runbook_url), startsAt.
   - Also call list_silences(org="` + org + `") to check whether the alert is silenced (and by whom).

2. If the alert has a ` + "`generatorURL`" + ` or the annotations mention a recording rule, call list_alert_rules with nameContains=<alertname> to get the rule's expression (so you can understand WHY it fires).

3. Correlate with logs (Loki):
   - Run query_loki_stats with a selector built from the alert's labels (e.g. ` + "`{namespace=\"X\", app=\"Y\"}`" + `) to check volume first.
   - If volume is reasonable (< ~1 GB over the window), run query_logs with the same selector + ` + "`|= \"error\"`" + ` or ` + "`| json | level=~\"error|warn\"`" + `, limit=100, over the last ` + lookback + `.
   - If no matching labels exist, try list_loki_label_values for the key ('namespace' or 'app') to find a close match.

4. Correlate with metrics (Mimir):
   - Pick one or two metrics that match the alert's domain. Start with list_prometheus_metric_names (prefix=<symptom-keyword>) or list_prometheus_metric_metadata (metric=...) to discover them.
   - Run query_metrics with ` + "`rate(X[5m])`" + ` or ` + "`sum by (...) (...)`" + ` over the last ` + lookback + `, step=1m.

5. Correlate with traces (Tempo) IF the alert concerns latency, error rate, or a specific service:
   - Run list_tempo_tag_values (tag='service.name', prefix=...) to confirm the service emits traces.
   - Run query_traces with TraceQL like ` + "`{ .service.name=\"<svc>\" && status=error }`" + ` limit=10.

6. Produce the report in this exact structure:
   - **What fires**: one sentence (alertname + severity + labels).
   - **Likely cause**: 1-3 bullets grounded in the data fetched above, each citing which tool result it came from.
   - **Recent logs**: up to 5 short excerpts (timestamp + message only).
   - **Recent metrics**: a one-line summary per metric you queried (current value, trend over window).
   - **Suggested next actions**: 2-4 bullets, concrete (who to page, which dashboard to open, which command to run). If the alert has a runbook_url, surface it.
   - **Open questions**: anything you couldn't determine from the available data.

Rules:
- Do NOT run queries without first using stats/discovery tools when the data could be large.
- Honour the response-size cap: if a tool returns { "error": "response_too_large" }, NARROW and retry (add label matchers, reduce time range, raise pageSize for discovery tools).
- Prefer aggregations (sum, rate, topk) over raw series.
- Do not suggest mutations (no silencing, no config changes) — this MCP is read-only.`

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Triage runbook for alert %s in org %s", fp, org),
		Messages: []mcp.PromptMessage{
			{Role: mcp.RoleUser, Content: mcp.TextContent{Type: "text", Text: system}},
		},
	}, nil
}
