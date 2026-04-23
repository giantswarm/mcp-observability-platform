// Package tools — dashboards_panels.go: panel-target resolution, template-
// variable expansion, datasource-kind inference, JSON Pointer, and the
// dashboard-query extraction used by run_panel_query /
// get_dashboard_panel_queries / get_dashboard_property.
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// grafanaTimeArg returns a Grafana-friendly time string (unix ms by default).
// When the named arg is set, it's passed through verbatim (already RFC3339,
// "now-1h", or a unix-ms numeral). When absent, returns now+offset in ms.
func grafanaTimeArg(req mcp.CallToolRequest, name string, offset time.Duration) string {
	if s := req.GetString(name, ""); s != "" {
		return s
	}
	return fmt.Sprintf("%d", time.Now().Add(offset).UnixMilli())
}

// panelTarget is the subset of a Grafana panel target we need. Different
// datasources spell the query differently (Prometheus uses Expr, Tempo uses
// Query), so we accept both fields.
type panelTarget struct {
	RefID      string          `json:"refId"`
	Expr       string          `json:"expr"`
	Query      string          `json:"query"`
	Datasource json.RawMessage `json:"datasource"`
}

// pickPanelTarget walks the dashboard, finds the panel by id, picks the
// requested target (by index, default 0), and resolves which kind of
// datasource it points at (mimir/loki/tempo). Datasource resolution prefers
// the panel-level datasource ref over the target-level one.
func pickPanelTarget(raw json.RawMessage, panelID, targetIdx int) (rawPanel, panelTarget, string, map[string]string, error) {
	var doc struct {
		Dashboard struct {
			Panels     []rawPanel    `json:"panels"`
			Templating rawTemplating `json:"templating"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}
	var found *rawPanel
	var walk func(ps []rawPanel)
	walk = func(ps []rawPanel) {
		for i := range ps {
			if ps[i].Type == panelTypeRow {
				walk(ps[i].Panels)
				continue
			}
			if ps[i].ID == panelID {
				found = &ps[i]
				return
			}
		}
	}
	walk(doc.Dashboard.Panels)
	if found == nil {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("panel %d not found", panelID)
	}
	if targetIdx < 0 || targetIdx >= len(found.Targets) {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("panel %d: targetIndex %d out of range (len=%d)", panelID, targetIdx, len(found.Targets))
	}
	var t panelTarget
	if err := json.Unmarshal(found.Targets[targetIdx], &t); err != nil {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("parse target: %w", err)
	}
	dsRaw := found.Datasource
	if len(dsRaw) == 0 || string(dsRaw) == "null" {
		dsRaw = t.Datasource
	}
	kind := datasourceKindFromRef(dsRaw, doc.Dashboard.Templating.List)
	vars := templateVarsToMap(doc.Dashboard.Templating.List)
	return *found, t, kind, vars, nil
}

// templateVarsToMap turns a dashboard's templating.list into {name: value}
// for use during panel-query variable substitution. Skips datasource
// variables (those resolve via datasourceKindFromRef instead). When a
// variable's current.value is the sentinel "$__all" or empty, we substitute
// `.+` so PromQL/LogQL regex matchers stay valid.
func templateVarsToMap(vars []rawTemplateVar) map[string]string {
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		if strings.EqualFold(v.Type, "datasource") {
			continue
		}
		raw, _ := v.Current.Value.(string)
		if raw == "" || raw == "$__all" {
			raw = ".+"
		}
		out[v.Name] = raw
	}
	return out
}

// expandGrafanaVars substitutes Grafana template macros and dashboard
// variables in `expr` so the resulting PromQL/LogQL/TraceQL is acceptable
// to Mimir/Loki/Tempo. Built-ins covered:
//
//	$__rate_interval / $__interval — defaults to step (or 5m).
//	$__interval_ms                 — step in ms (or 300000).
//	$__range / $__range_s / $__range_ms — end-start as a duration.
//
// Dashboard variables are taken from `vars` (sourced from templating.list).
// Substitution is purely textual; values are not URL-encoded.
func expandGrafanaVars(expr string, vars map[string]string, start, end, step string) string {
	intvl := step
	if intvl == "" {
		intvl = "5m"
	}
	intvlMs := durationToMillis(intvl)
	rng := computeRangeDuration(start, end)

	// Built-ins first (longer names before shorter to avoid prefix collisions).
	replacements := []struct{ from, to string }{
		{"$__rate_interval", intvl},
		{"${__rate_interval}", intvl},
		{"$__interval_ms", intvlMs},
		{"${__interval_ms}", intvlMs},
		{"$__interval", intvl},
		{"${__interval}", intvl},
		{"$__range_ms", durationToMillis(rng)},
		{"${__range_ms}", durationToMillis(rng)},
		{"$__range_s", strconv.FormatInt(durationToSeconds(rng), 10)},
		{"${__range_s}", strconv.FormatInt(durationToSeconds(rng), 10)},
		{"$__range", rng},
		{"${__range}", rng},
	}
	for _, r := range replacements {
		expr = strings.ReplaceAll(expr, r.from, r.to)
	}
	// Dashboard variables. Two-pass: `${name}` forms first (unambiguous,
	// brace-delimited), then `$name` forms sorted by length DESC so e.g.
	// `$cluster_id` replaces before `$cluster` (otherwise map-iteration
	// order could turn `$cluster_id` into `<val>_id`).
	names := make([]string, 0, len(vars))
	for name := range vars {
		expr = strings.ReplaceAll(expr, "${"+name+"}", vars[name])
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, name := range names {
		expr = strings.ReplaceAll(expr, "$"+name, vars[name])
	}
	return expr
}

// durationToMillis returns "<n>" milliseconds for a Prometheus-shaped
// duration string (e.g. "5m" -> "300000"). Returns "300000" on parse error.
func durationToMillis(d string) string {
	td, err := time.ParseDuration(d)
	if err != nil {
		return "300000"
	}
	return strconv.FormatInt(td.Milliseconds(), 10)
}

func durationToSeconds(d string) int64 {
	td, err := time.ParseDuration(d)
	if err != nil {
		return 300
	}
	return int64(td.Seconds())
}

// computeRangeDuration turns start+end (RFC3339 or unix epoch seconds) into
// a duration string. Defaults to "1h" when start/end aren't both set.
func computeRangeDuration(start, end string) string {
	s := parseGrafanaTime(start)
	e := parseGrafanaTime(end)
	if s.IsZero() || e.IsZero() || !e.After(s) {
		return "1h"
	}
	return e.Sub(s).Round(time.Second).String()
}

func parseGrafanaTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// unix seconds (or ms — accept both by magnitude check)
		if n > 1e12 {
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	return time.Time{}
}

// rawTemplating is the dashboard's templating section. We read it to resolve
// datasource template variables like `$datasource` → the variable's declared
// datasource type.
type rawTemplating struct {
	List []rawTemplateVar `json:"list"`
}

type rawTemplateVar struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Query   any    `json:"query"` // for type=datasource this is the DS type string
	Current struct {
		Value any `json:"value"` // the chosen value (string for single-select; usually a string)
	} `json:"current"`
}

// datasourceKindFromRef extracts a coarse datasource kind from a Grafana
// datasource reference. Accepts three shapes + a template variable:
//
//   - object: {"type":"prometheus","uid":"..."}
//   - bare string uid that contains the type keyword
//   - "$varName" — resolved against the dashboard's templating.list: the
//     variable's `query` field carries the datasource type for type=datasource
//     variables.
//
// Returns "" if none of the above match.
func datasourceKindFromRef(raw json.RawMessage, templates []rawTemplateVar) string {
	if len(raw) == 0 {
		return ""
	}
	// String form.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.HasPrefix(s, "$") || strings.HasPrefix(s, "${") {
			name := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}"), "$")
			for _, v := range templates {
				if v.Name == name && strings.EqualFold(v.Type, "datasource") {
					if qs, ok := v.Query.(string); ok {
						return kindFromTypeString(qs)
					}
				}
			}
			return ""
		}
		return kindFromTypeString(s)
	}
	// Object form: {"type":"prometheus","uid":"..."}
	var obj struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	if k := kindFromTypeString(obj.Type); k != "" {
		return k
	}
	return kindFromTypeString(obj.UID)
}

// kindFromTypeString maps a Grafana datasource type string to our internal
// kind (mimir/loki/tempo). Also handles uid substrings as a fallback.
func kindFromTypeString(s string) string {
	ls := strings.ToLower(s)
	switch {
	case ls == "prometheus" || ls == dsKindMimir ||
		strings.Contains(ls, dsKindMimir) || strings.Contains(ls, "prometheus"):
		return dsKindMimir
	case strings.Contains(ls, dsKindLoki):
		return dsKindLoki
	case strings.Contains(ls, dsKindTempo):
		return dsKindTempo
	}
	return ""
}

// readJSONPointer resolves an RFC 6901 JSON Pointer against a JSON document
// and returns the sub-tree re-serialised as JSON. The empty pointer returns
// the whole document. Arrays are indexed by decimal number; the "-" segment
// is not supported (no way to append on a read).
func readJSONPointer(doc []byte, pointer string) ([]byte, error) {
	if pointer == "" || pointer == "/" {
		return doc, nil
	}
	if pointer[0] != '/' {
		return nil, fmt.Errorf("invalid pointer %q: must start with '/'", pointer)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}
	for raw := range strings.SplitSeq(pointer[1:], "/") {
		// RFC 6901 escapes: ~1 = /, ~0 = ~
		tok := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch cur := v.(type) {
		case map[string]any:
			next, ok := cur[tok]
			if !ok {
				return nil, fmt.Errorf("pointer %q: key %q not found", pointer, tok)
			}
			v = next
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil {
				return nil, fmt.Errorf("pointer %q: expected array index, got %q", pointer, tok)
			}
			if idx < 0 || idx >= len(cur) {
				return nil, fmt.Errorf("pointer %q: index %d out of range (len=%d)", pointer, idx, len(cur))
			}
			v = cur[idx]
		default:
			return nil, fmt.Errorf("pointer %q: segment %q traverses non-container (%T)", pointer, tok, cur)
		}
	}
	return json.Marshal(v)
}

// rawPanel is the subset of the Grafana panel shape we decode. Targets are
// retained as json.RawMessage because Grafana shells out query expressions in
// different fields per datasource type (expr / query / rawSql / queryText…).
type rawPanel struct {
	ID         int               `json:"id"`
	Type       string            `json:"type"`
	Title      string            `json:"title"`
	Targets    []json.RawMessage `json:"targets"`
	Panels     []rawPanel        `json:"panels"`
	Datasource json.RawMessage   `json:"datasource"`
}

// extractDashboardQueries walks a dashboard's panels and returns the raw
// query expressions per panel. Filters by panelID (exact match) or
// titleContains (case-insensitive substring) when non-empty/positive.
func extractDashboardQueries(raw json.RawMessage, panelID int, titleContains string) (any, error) {
	var doc struct {
		Dashboard struct {
			UID    string     `json:"uid"`
			Title  string     `json:"title"`
			Panels []rawPanel `json:"panels"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}

	type query struct {
		RefID      string `json:"refId,omitempty"`
		Expr       string `json:"expr,omitempty"`      // Prometheus
		Query      string `json:"query,omitempty"`     // many
		RawSQL     string `json:"rawSql,omitempty"`    // SQL-ish
		QueryText  string `json:"queryText,omitempty"` // some
		Datasource any    `json:"datasource,omitempty"`
	}
	type panelOut struct {
		ID         int     `json:"id"`
		Title      string  `json:"title"`
		Type       string  `json:"type"`
		Datasource any     `json:"datasource,omitempty"`
		Queries    []query `json:"queries"`
	}
	out := []panelOut{}
	var walk func(ps []rawPanel)
	titleLC := strings.ToLower(titleContains)
	walk = func(ps []rawPanel) {
		for _, p := range ps {
			if p.Type == panelTypeRow {
				walk(p.Panels)
				continue
			}
			if panelID > 0 && p.ID != panelID {
				continue
			}
			if titleContains != "" && !strings.Contains(strings.ToLower(p.Title), titleLC) {
				continue
			}
			po := panelOut{ID: p.ID, Title: p.Title, Type: p.Type}
			if len(p.Datasource) > 0 {
				var ds any
				_ = json.Unmarshal(p.Datasource, &ds)
				po.Datasource = ds
			}
			for _, t := range p.Targets {
				var q query
				_ = json.Unmarshal(t, &q)
				po.Queries = append(po.Queries, q)
			}
			out = append(out, po)
		}
	}
	walk(doc.Dashboard.Panels)

	return struct {
		UID    string     `json:"uid"`
		Title  string     `json:"title"`
		Count  int        `json:"count"`
		Panels []panelOut `json:"panels"`
	}{UID: doc.Dashboard.UID, Title: doc.Dashboard.Title, Count: len(out), Panels: out}, nil
}
