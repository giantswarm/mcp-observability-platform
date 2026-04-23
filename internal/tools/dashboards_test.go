package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
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

// TestExpandGrafanaVars_LengthDescSort is the regression guard for the
// subtle bug where `$cluster` replaces before `$cluster_id` and corrupts
// the longer name to `<val>_id`. The length-DESC sort in expandGrafanaVars
// prevents this; reordering the sort direction would break here.
func TestExpandGrafanaVars_LengthDescSort(t *testing.T) {
	vars := map[string]string{
		"cluster":    "prod",
		"cluster_id": "42",
	}
	// Query uses both — the longer name must NOT be consumed by the shorter
	// one's replacement.
	got := expandGrafanaVars(`rate(up{cluster="$cluster",id="$cluster_id"}[5m])`, vars, "", "", "1m")
	want := `rate(up{cluster="prod",id="42"}[5m])`
	if got != want {
		t.Errorf("length-DESC sort regression:\n got = %q\nwant = %q", got, want)
	}
}

func TestExpandGrafanaVars_BraceAndBareForms(t *testing.T) {
	vars := map[string]string{"env": "prod"}
	cases := []struct {
		expr, want string
	}{
		{`up{env="$env"}`, `up{env="prod"}`},
		{`up{env="${env}"}`, `up{env="prod"}`},
		{`sum by (env) (up) / on(env) ${env}`, `sum by (env) (up) / on(env) prod`},
	}
	for _, c := range cases {
		if got := expandGrafanaVars(c.expr, vars, "", "", "1m"); got != c.want {
			t.Errorf("expand(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestExpandGrafanaVars_BuiltIns(t *testing.T) {
	// Built-ins resolve to step / computed range. Empty step defaults to 5m.
	cases := []struct {
		expr, start, end, step string
		want                   string
	}{
		{`rate(up[$__rate_interval])`, "", "", "30s", `rate(up[30s])`},
		{`rate(up[$__interval])`, "", "", "", `rate(up[5m])`}, // default step = 5m
		{`$__interval_ms`, "", "", "1m", "60000"},
		{`$__range`, "2024-01-01T00:00:00Z", "2024-01-01T01:00:00Z", "", "1h0m0s"},
		{`$__range_s`, "2024-01-01T00:00:00Z", "2024-01-01T01:00:00Z", "", "3600"},
	}
	for _, c := range cases {
		if got := expandGrafanaVars(c.expr, nil, c.start, c.end, c.step); got != c.want {
			t.Errorf("expand(%q step=%q) = %q, want %q", c.expr, c.step, got, c.want)
		}
	}
}

func TestParseGrafanaTime(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		check   func(time.Time) bool
	}{
		{"", false, func(t time.Time) bool { return t.IsZero() }},
		{"2024-05-20T10:00:00Z", false, func(tt time.Time) bool {
			return tt.Equal(time.Date(2024, 5, 20, 10, 0, 0, 0, time.UTC))
		}},
		{"1700000000", false, func(tt time.Time) bool {
			// Unix seconds
			return tt.Equal(time.Unix(1700000000, 0))
		}},
		{"1700000000000", false, func(tt time.Time) bool {
			// Unix millis (magnitude > 1e12)
			return tt.Equal(time.UnixMilli(1700000000000))
		}},
		{"not-a-time", false, func(tt time.Time) bool { return tt.IsZero() }},
	}
	for _, c := range cases {
		got := parseGrafanaTime(c.in)
		if !c.check(got) {
			t.Errorf("parseGrafanaTime(%q) = %v, fails check", c.in, got)
		}
	}
}

func TestReadJSONPointer_HappyPath(t *testing.T) {
	doc := []byte(`{"panels":[{"id":1,"title":"A"},{"id":2,"title":"B"}],"meta":{"tags":["x","y"]}}`)
	cases := []struct {
		pointer, want string
	}{
		{"", string(doc)},     // empty → whole doc
		{"/", string(doc)},    // root → whole doc
		{"/panels/0/id", "1"}, // array index then key
		{"/panels/1/title", `"B"`},
		{"/meta/tags", `["x","y"]`},
		{"/meta/tags/1", `"y"`},
	}
	for _, c := range cases {
		got, err := readJSONPointer(doc, c.pointer)
		if err != nil {
			t.Errorf("readJSONPointer(%q) err: %v", c.pointer, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("readJSONPointer(%q) = %s, want %s", c.pointer, got, c.want)
		}
	}
}

// TestReadJSONPointer_RFC6901Escapes covers the two escape sequences that
// are easy to get wrong: ~1 = / and ~0 = ~. Order matters — unescape ~1
// first then ~0, otherwise "~01" (meaning "~1" literal) becomes "/".
func TestReadJSONPointer_RFC6901Escapes(t *testing.T) {
	doc := []byte(`{"a/b":"slash","c~d":"tilde","~1":"literal-slash-key"}`)
	cases := []struct {
		pointer, want string
	}{
		{"/a~1b", `"slash"`},            // /a/b literal → escape / as ~1
		{"/c~0d", `"tilde"`},            // /c~d literal → escape ~ as ~0
		{"/~01", `"literal-slash-key"`}, // /~1 key literal (tilde 0 = ~, then 1) ... actually this is tricky
	}
	for _, c := range cases {
		got, err := readJSONPointer(doc, c.pointer)
		if err != nil {
			t.Errorf("readJSONPointer(%q) err: %v", c.pointer, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("readJSONPointer(%q) = %s, want %s", c.pointer, got, c.want)
		}
	}
}

func TestReadJSONPointer_ErrorCases(t *testing.T) {
	doc := []byte(`{"panels":[{"id":1}],"leaf":5}`)
	cases := []struct {
		pointer, wantErrSubstring string
	}{
		{"no-leading-slash", "must start with '/'"},
		{"/missing", "not found"},
		{"/panels/notanum", "expected array index"},
		{"/panels/99", "out of range"},
		{"/leaf/further", "traverses non-container"},
	}
	for _, c := range cases {
		_, err := readJSONPointer(doc, c.pointer)
		if err == nil {
			t.Errorf("readJSONPointer(%q) = nil err, want error containing %q", c.pointer, c.wantErrSubstring)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErrSubstring) {
			t.Errorf("readJSONPointer(%q) err = %v, want substring %q", c.pointer, err, c.wantErrSubstring)
		}
	}
}

func TestDatasourceKindFromRef(t *testing.T) {
	templates := []rawTemplateVar{
		{Name: "ds", Type: "datasource", Query: "loki"},
		{Name: "metrics", Type: "datasource", Query: "prometheus"},
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"object form prometheus", `{"type":"prometheus","uid":"mimir-prod"}`, "mimir"},
		{"object form loki", `{"type":"loki","uid":"loki-prod"}`, "loki"},
		{"object form tempo", `{"type":"tempo","uid":"tempo-prod"}`, "tempo"},
		{"object form unknown type, uid hint", `{"type":"other","uid":"mimir-cluster"}`, "mimir"},
		{"bare string uid with mimir", `"mimir-prod"`, "mimir"},
		{"bare string prometheus exact", `"prometheus"`, "mimir"},
		{"template var $ds → loki", `"$ds"`, "loki"},
		{"template var ${metrics} → mimir", `"${metrics}"`, "mimir"},
		{"template var unknown name", `"$doesNotExist"`, ""},
		{"empty raw", ``, ""},
		{"junk raw", `"unrelated"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := datasourceKindFromRef(json.RawMessage(c.raw), templates)
			if got != c.want {
				t.Errorf("datasourceKindFromRef(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestTemplateVarsToMap_SkipsDatasourceVarsAndHandlesAll(t *testing.T) {
	vars := []rawTemplateVar{
		{Name: "ds", Type: "datasource", Query: "prometheus"},
	}
	// Non-datasource vars: a plain one, an $__all var (should become ".+")
	// and an unset one (also becomes ".+"). Current.Value is an `any` — plug
	// in concrete types the code expects.
	cluster := rawTemplateVar{Name: "cluster", Type: "custom"}
	cluster.Current.Value = "prod"
	env := rawTemplateVar{Name: "env", Type: "custom"}
	env.Current.Value = "$__all"
	missing := rawTemplateVar{Name: "missing", Type: "custom"}
	missing.Current.Value = ""
	vars = append(vars, cluster, env, missing)

	got := templateVarsToMap(vars)

	if _, has := got["ds"]; has {
		t.Errorf("datasource-typed var should be skipped: %v", got)
	}
	if got["cluster"] != "prod" {
		t.Errorf("cluster = %q, want prod", got["cluster"])
	}
	if got["env"] != ".+" {
		t.Errorf("$__all should become regex '.+', got %q", got["env"])
	}
	if got["missing"] != ".+" {
		t.Errorf("empty value should default to '.+', got %q", got["missing"])
	}
}

func TestGrafanaTimeArg_ProvidedValueWins(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"start": "2024-01-01T00:00:00Z"}}}
	got := grafanaTimeArg(req, "start", -1*time.Hour)
	if got != "2024-01-01T00:00:00Z" {
		t.Errorf("provided start should pass through, got %q", got)
	}
}

func TestGrafanaTimeArg_MissingValueUsesOffsetFromNow(t *testing.T) {
	// With offset = -1h, the result should be a unix-millis stamp roughly
	// 1 hour in the past. Allow generous slack.
	before := time.Now().Add(-1 * time.Hour).Add(-5 * time.Second).UnixMilli()
	after := time.Now().Add(-1 * time.Hour).Add(5 * time.Second).UnixMilli()
	got := grafanaTimeArg(mcp.CallToolRequest{}, "start", -1*time.Hour)
	stamp, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("grafanaTimeArg output %q is not an integer: %v", got, err)
	}
	if stamp < before || stamp > after {
		t.Errorf("grafanaTimeArg offset = %d, want between %d and %d", stamp, before, after)
	}
}

func TestPickPanelTarget_FindsNestedInRow(t *testing.T) {
	// Dashboard with a row containing panel id 2; pickPanelTarget should
	// descend through rows and find it.
	doc := json.RawMessage(`{
		"dashboard":{
			"panels":[
				{"id":1,"type":"timeseries","datasource":{"type":"prometheus","uid":"mimir"},"targets":[{"refId":"A","expr":"up"}]},
				{"id":10,"type":"row","panels":[
					{"id":2,"type":"timeseries","datasource":{"type":"loki","uid":"loki"},"targets":[{"refId":"A","query":"{job=\"x\"}"}]}
				]}
			],
			"templating":{"list":[]}
		}
	}`)
	panel, target, kind, _, err := pickPanelTarget(doc, 2, 0)
	if err != nil {
		t.Fatalf("pickPanelTarget: %v", err)
	}
	if panel.ID != 2 {
		t.Errorf("panel.ID = %d, want 2", panel.ID)
	}
	if target.Query != `{job="x"}` {
		t.Errorf("target.Query = %q, want {job=\"x\"}", target.Query)
	}
	if kind != "loki" {
		t.Errorf("kind = %q, want loki", kind)
	}
}

func TestPickPanelTarget_NotFound(t *testing.T) {
	doc := json.RawMessage(`{"dashboard":{"panels":[{"id":1}],"templating":{"list":[]}}}`)
	_, _, _, _, err := pickPanelTarget(doc, 999, 0)
	if err == nil {
		t.Fatal("expected error for missing panel id")
	}
}

func TestPickPanelTarget_TargetIdxOutOfRange(t *testing.T) {
	doc := json.RawMessage(`{"dashboard":{"panels":[
		{"id":1,"type":"timeseries","datasource":{"type":"prometheus"},"targets":[{"refId":"A","expr":"up"}]}
	],"templating":{"list":[]}}}`)
	_, _, _, _, err := pickPanelTarget(doc, 1, 5)
	if err == nil {
		t.Fatal("expected error for targetIdx out of range")
	}
}
