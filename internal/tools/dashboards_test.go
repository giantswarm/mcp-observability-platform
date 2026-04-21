package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGroupDashboardsByFolder_BasicGrouping(t *testing.T) {
	raw := json.RawMessage(`[
		{"uid":"a","title":"Alpha","url":"/d/a","type":"dash-db","folderTitle":"Ops"},
		{"uid":"b","title":"Bravo","url":"/d/b","type":"dash-db","folderTitle":"Ops"},
		{"uid":"c","title":"Charlie","url":"/d/c","type":"dash-db","folderTitle":""},
		{"uid":"d","title":"Delta","url":"/d/d","type":"dash-folder","folderTitle":""}
	]`)
	res, err := groupDashboardsByFolder(raw, "", 0, 100)
	if err != nil {
		t.Fatalf("groupDashboardsByFolder: %v", err)
	}
	b, _ := json.Marshal(res)
	var out struct {
		Total   int `json:"total"`
		Folders []struct {
			Title string `json:"title"`
			Count int    `json:"count"`
		} `json:"folders"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Folders: "dash-folder" entries are filtered; (no folder) last.
	if out.Total != 3 {
		t.Errorf("total = %d, want 3 (dash-folder must be filtered)", out.Total)
	}
	if len(out.Folders) != 2 {
		t.Fatalf("folders = %d, want 2", len(out.Folders))
	}
	if out.Folders[0].Title != "Ops" {
		t.Errorf("first folder = %q, want 'Ops'", out.Folders[0].Title)
	}
	if out.Folders[1].Title != "(no folder)" {
		t.Errorf("last folder = %q, want '(no folder)'", out.Folders[1].Title)
	}
}

func TestGroupDashboardsByFolder_FolderFilterCaseInsensitive(t *testing.T) {
	raw := json.RawMessage(`[
		{"uid":"a","title":"A","type":"dash-db","folderTitle":"Platform"},
		{"uid":"b","title":"B","type":"dash-db","folderTitle":"Security"}
	]`)
	res, err := groupDashboardsByFolder(raw, "PLATFORM", 0, 100)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	b, _ := json.Marshal(res)
	var out struct {
		Folders []struct {
			Title string `json:"title"`
			Count int    `json:"count"`
		} `json:"folders"`
	}
	_ = json.Unmarshal(b, &out)
	if len(out.Folders) != 1 || out.Folders[0].Title != "Platform" {
		t.Errorf("filter=PLATFORM got %v, want one folder 'Platform'", out.Folders)
	}
}

func TestGroupDashboardsByFolder_Pagination(t *testing.T) {
	// 5 folders with 1 dash each; pageSize 2 → 3 pages.
	items := []byte(`[`)
	for i := range 5 {
		if i > 0 {
			items = append(items, ',')
		}
		items = append(items, []byte(
			`{"uid":"u`+string(rune('a'+i))+`","title":"T","type":"dash-db","folderTitle":"F`+string(rune('a'+i))+`"}`)...)
	}
	items = append(items, ']')
	res, err := groupDashboardsByFolder(items, "", 0, 2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	b, _ := json.Marshal(res)
	var out struct {
		TotalFolders int  `json:"totalFolders"`
		HasMore      bool `json:"hasMore"`
		Folders      []struct {
			Title string `json:"title"`
		} `json:"folders"`
	}
	_ = json.Unmarshal(b, &out)
	if out.TotalFolders != 5 || !out.HasMore || len(out.Folders) != 2 {
		t.Errorf("page 0: got totalFolders=%d hasMore=%v len=%d, want 5 true 2", out.TotalFolders, out.HasMore, len(out.Folders))
	}
}

func TestSummariseDashboard(t *testing.T) {
	raw := json.RawMessage(`{
		"dashboard": {
			"uid": "x",
			"title": "Example",
			"tags": ["ops"],
			"refresh": "30s",
			"templating": {
				"list": [{"name":"cluster","label":"Cluster","type":"query","current":{"value":"prod"}}]
			},
			"panels": [
				{"id":1,"type":"row","title":"Row 1","panels":[
					{"id":2,"type":"graph","title":"CPU"}
				]},
				{"id":3,"type":"stat","title":"Up"}
			]
		},
		"meta": {"url":"/d/x","version":5,"updated":"2026-04-18T12:00:00Z"}
	}`)
	got, err := summariseDashboard(raw)
	if err != nil {
		t.Fatalf("summariseDashboard: %v", err)
	}
	b, _ := json.Marshal(got)
	s := string(b)
	// Should NOT contain any query targets (we project away queries).
	if strings.Contains(s, `"targets"`) || strings.Contains(s, `"expr"`) {
		t.Errorf("summary leaked query details: %s", s)
	}
	// Must contain variable name and panel titles.
	for _, want := range []string{`"cluster"`, `"CPU"`, `"Up"`, `"Row 1"`, `"totalPanels":2`} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q: %s", want, s)
		}
	}
}

func TestExtractDashboardQueries_FilterByPanelID(t *testing.T) {
	raw := json.RawMessage(`{
		"dashboard": {
			"uid": "x",
			"title": "Ex",
			"panels": [
				{"id":1,"type":"row","title":"R","panels":[
					{"id":2,"type":"graph","title":"A","targets":[{"refId":"A","expr":"up"}]},
					{"id":3,"type":"graph","title":"B","targets":[{"refId":"A","expr":"rate(x[5m])"}]}
				]}
			]
		}
	}`)
	got, err := extractDashboardQueries(raw, 3, "")
	if err != nil {
		t.Fatalf("extractDashboardQueries: %v", err)
	}
	b, _ := json.Marshal(got)
	s := string(b)
	if !strings.Contains(s, `"rate(x[5m])"`) || strings.Contains(s, `"up"`) {
		t.Errorf("panelId=3 filter got %s", s)
	}
}

func TestExtractDashboardQueries_FilterByTitleContains(t *testing.T) {
	raw := json.RawMessage(`{
		"dashboard": {
			"panels": [
				{"id":1,"type":"graph","title":"CPU Usage","targets":[{"refId":"A","expr":"cpu"}]},
				{"id":2,"type":"graph","title":"Memory","targets":[{"refId":"A","expr":"mem"}]}
			]
		}
	}`)
	got, err := extractDashboardQueries(raw, 0, "cpu")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	b, _ := json.Marshal(got)
	s := string(b)
	if !strings.Contains(s, `"cpu"`) || strings.Contains(s, `"mem"`) {
		t.Errorf("titleContains=cpu got %s", s)
	}
}
