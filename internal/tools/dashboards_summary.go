// Package tools — dashboards_summary.go: compact projections of the full
// Grafana dashboard JSON used by search_dashboards / get_dashboard_summary.
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// groupDashboardsByFolder transforms Grafana's flat /api/search response into
// a compact folder tree. Dropping unused fields and grouping typically shrinks
// payload from O(dashboards × 400 bytes) to O(dashboards × 80 bytes).
//
// If folderFilter is non-empty, only that folder is returned (case-insensitive
// match; the sentinel "(no folder)" matches root-level dashboards).
// When the folder count exceeds pageSize, the result is sliced and a
// nextPage hint is set.
func groupDashboardsByFolder(raw json.RawMessage, folderFilter string, page, pageSize int) (any, error) {
	type item struct {
		UID         string `json:"uid"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		FolderTitle string `json:"folderTitle"`
		Type        string `json:"type"`
	}
	var items []item
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("unmarshal dashboards: %w", err)
	}

	type dashEntry struct {
		Title string `json:"title"`
		UID   string `json:"uid"`
		URL   string `json:"url,omitempty"`
	}
	const noFolder = "(no folder)"
	byFolder := map[string][]dashEntry{}
	for _, it := range items {
		if it.Type != "" && it.Type != "dash-db" {
			continue
		}
		f := it.FolderTitle
		if f == "" {
			f = noFolder
		}
		byFolder[f] = append(byFolder[f], dashEntry{Title: it.Title, UID: it.UID, URL: it.URL})
	}

	if folderFilter != "" {
		key := folderFilter
		// case-insensitive folder match
		var match string
		for k := range byFolder {
			if strings.EqualFold(k, key) {
				match = k
				break
			}
		}
		if match == "" {
			byFolder = map[string][]dashEntry{}
		} else {
			byFolder = map[string][]dashEntry{match: byFolder[match]}
		}
	}

	type folderView struct {
		Title      string      `json:"title"`
		Count      int         `json:"count"`
		Dashboards []dashEntry `json:"dashboards"`
	}
	folders := make([]folderView, 0, len(byFolder))
	total := 0
	for name, ds := range byFolder {
		sort.Slice(ds, func(i, j int) bool { return ds[i].Title < ds[j].Title })
		folders = append(folders, folderView{Title: name, Count: len(ds), Dashboards: ds})
		total += len(ds)
	}
	sort.Slice(folders, func(i, j int) bool {
		// Put "(no folder)" last so folder-organised content reads top-down.
		if folders[i].Title == noFolder {
			return false
		}
		if folders[j].Title == noFolder {
			return true
		}
		return strings.ToLower(folders[i].Title) < strings.ToLower(folders[j].Title)
	})

	if pageSize <= 0 {
		pageSize = 20
	}
	pageSize = clampInt(pageSize, 1, 200)
	if page < 0 {
		page = 0
	}
	start := min(page*pageSize, len(folders))
	end := min(start+pageSize, len(folders))

	return struct {
		Total        int          `json:"total"`
		TotalFolders int          `json:"totalFolders"`
		Page         int          `json:"page"`
		PageSize     int          `json:"pageSize"`
		HasMore      bool         `json:"hasMore"`
		Folders      []folderView `json:"folders"`
	}{
		Total:        total,
		TotalFolders: len(folders),
		Page:         page,
		PageSize:     pageSize,
		HasMore:      end < len(folders),
		Folders:      folders[start:end],
	}, nil
}

// summariseDashboard projects the full dashboard JSON to a compact overview:
// metadata, template variables (with defaults), and a row/panel tree with
// titles and types but NO queries. Typical size: 1-3% of full dashboard JSON.
func summariseDashboard(raw json.RawMessage) (any, error) {
	var doc struct {
		Dashboard struct {
			UID   string   `json:"uid"`
			Title string   `json:"title"`
			Tags  []string `json:"tags"`
			// Grafana permits either a string ("30s") or boolean false (disabled).
			// Use RawMessage and render as a string below.
			Refresh    json.RawMessage `json:"refresh"`
			Templating struct {
				List []struct {
					Name    string `json:"name"`
					Label   string `json:"label"`
					Type    string `json:"type"`
					Current struct {
						Value any `json:"value"`
					} `json:"current"`
				} `json:"list"`
			} `json:"templating"`
			Panels []rawPanel `json:"panels"`
		} `json:"dashboard"`
		Meta struct {
			URL       string `json:"url"`
			FolderID  int    `json:"folderId"`
			FolderURL string `json:"folderUrl"`
			Version   int    `json:"version"`
			Updated   string `json:"updated"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}

	type panelSummary struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	type rowSummary struct {
		ID     int            `json:"id,omitempty"`
		Title  string         `json:"title,omitempty"`
		Panels []panelSummary `json:"panels"`
	}
	var rows []rowSummary
	flushRow := func(r *rowSummary) {
		if r != nil && (r.Title != "" || len(r.Panels) > 0) {
			rows = append(rows, *r)
		}
	}

	// Grafana dashboards: panels live either flat (modern) or nested under
	// type:"row" panels (legacy). We handle both.
	if len(doc.Dashboard.Panels) > 0 {
		var cur *rowSummary
		for _, p := range doc.Dashboard.Panels {
			if p.Type == panelTypeRow {
				flushRow(cur)
				r := rowSummary{ID: p.ID, Title: p.Title}
				for _, nested := range p.Panels {
					r.Panels = append(r.Panels, panelSummary{ID: nested.ID, Title: nested.Title, Type: nested.Type})
				}
				cur = &r
				continue
			}
			if cur == nil {
				cur = &rowSummary{}
			}
			cur.Panels = append(cur.Panels, panelSummary{ID: p.ID, Title: p.Title, Type: p.Type})
		}
		flushRow(cur)
	}

	type tmplVar struct {
		Name    string `json:"name"`
		Label   string `json:"label,omitempty"`
		Type    string `json:"type"`
		Current any    `json:"current,omitempty"`
	}
	vars := make([]tmplVar, 0, len(doc.Dashboard.Templating.List))
	for _, v := range doc.Dashboard.Templating.List {
		vars = append(vars, tmplVar{Name: v.Name, Label: v.Label, Type: v.Type, Current: v.Current.Value})
	}

	return struct {
		UID         string       `json:"uid"`
		Title       string       `json:"title"`
		Tags        []string     `json:"tags,omitempty"`
		Refresh     string       `json:"refresh,omitempty"`
		URL         string       `json:"url,omitempty"`
		Version     int          `json:"version,omitempty"`
		Updated     string       `json:"updated,omitempty"`
		Variables   []tmplVar    `json:"variables,omitempty"`
		Rows        []rowSummary `json:"rows"`
		TotalPanels int          `json:"totalPanels"`
	}{
		UID:         doc.Dashboard.UID,
		Title:       doc.Dashboard.Title,
		Tags:        doc.Dashboard.Tags,
		Refresh:     refreshToString(doc.Dashboard.Refresh),
		URL:         doc.Meta.URL,
		Version:     doc.Meta.Version,
		Updated:     doc.Meta.Updated,
		Variables:   vars,
		Rows:        rows,
		TotalPanels: countPanels(doc.Dashboard.Panels),
	}, nil
}

// refreshToString renders Grafana's polymorphic "refresh" field (string or
// bool) as a single string: "30s" stays as "30s", `false` (disabled) becomes
// "". Returns "" for any other shape — the string-or-bool decode covers
// every valid Grafana dashboard export.
func refreshToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func countPanels(ps []rawPanel) int {
	n := 0
	for _, p := range ps {
		if p.Type == panelTypeRow {
			n += countPanels(p.Panels)
			continue
		}
		n++
	}
	return n
}
