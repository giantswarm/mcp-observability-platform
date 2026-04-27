package tools

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBuildSlowRequestsTraceQL(t *testing.T) {
	cases := []struct {
		name       string
		service    string
		minDur     time.Duration
		errorsOnly bool
		want       string
	}{
		{"basic", "api", 1 * time.Second, false, `{ resource.service.name = "api" && duration > 1s }`},
		{"errors only", "api", 500 * time.Millisecond, true, `{ resource.service.name = "api" && duration > 500ms && status = error }`},
		{"service with quote", `weird"name`, 2 * time.Second, false, `{ resource.service.name = "weird\"name" && duration > 2s }`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildSlowRequestsTraceQL(c.service, c.minDur, c.errorsOnly)
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestParseDurationOrDefault(t *testing.T) {
	def := 5 * time.Minute
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", def, false},
		{"15m", 15 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"not-a-duration", 0, true},
		{"-1s", 0, true},
		{"0", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseDurationOrDefault(c.in, def)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{256 << 20, "256.0 MiB"},
		{2 * 1024 * 1024 * 1024, "2.0 GiB"},
	}
	for _, c := range cases {
		got := humanBytes(c.n)
		if got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestHandler_FindErrorPatternLogs covers the full pipeline: label probe to
// pick service_name, stats size check, then query_range with the assembled
// selector. Same fake-Grafana shape used by handler_integration_test.go.
func TestHandler_FindErrorPatternLogs(t *testing.T) {
	var (
		labelProbes []string
		sawSelector string
		sawStatsHit bool
		sawRangeHit bool
	)
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/loki/api/v1/label/"):
			parts := strings.Split(r.URL.Path, "/")
			labelProbes = append(labelProbes, parts[len(parts)-2])
			lastLabel := parts[len(parts)-2]
			values := `[]`
			if lastLabel == "service_name" {
				values = `["api","worker"]`
			}
			_, _ = w.Write([]byte(`{"status":"success","data":` + values + `}`))
		case strings.HasSuffix(r.URL.Path, "/loki/api/v1/index/stats"):
			sawStatsHit = true
			sawSelector = r.URL.Query().Get("query")
			_, _ = w.Write([]byte(`{"streams":1,"chunks":1,"bytes":1024,"entries":10}`))
		case strings.HasSuffix(r.URL.Path, "/loki/api/v1/query_range"):
			sawRangeHit = true
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
		default:
			http.NotFound(w, r)
		}
	})
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "find_error_pattern_logs", map[string]any{
		"org": "acme", "service": "api",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if len(labelProbes) == 0 || labelProbes[0] != "service_name" {
		t.Errorf("first label probe = %v, want service_name first", labelProbes)
	}
	if !sawStatsHit {
		t.Error("loki stats endpoint was not hit")
	}
	if !sawRangeHit {
		t.Error("loki query_range endpoint was not hit")
	}
	if !strings.Contains(sawSelector, `service_name="api"`) {
		t.Errorf("stats query missing service selector: %q", sawSelector)
	}
	if !strings.Contains(sawSelector, "(error|fail|fatal|panic|exception|traceback)") {
		t.Errorf("stats query missing error keyword filter: %q", sawSelector)
	}
}

// TestHandler_FindErrorPatternLogs_TooLarge asserts the size-guard refuses to
// fire query_range when stats exceeds the 256 MiB cap.
func TestHandler_FindErrorPatternLogs_TooLarge(t *testing.T) {
	var sawRange bool
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/loki/api/v1/label/"):
			_, _ = w.Write([]byte(`{"status":"success","data":["api"]}`))
		case strings.HasSuffix(r.URL.Path, "/loki/api/v1/index/stats"):
			_, _ = w.Write([]byte(`{"bytes":1099511627776}`)) // 1 TiB
		case strings.HasSuffix(r.URL.Path, "/loki/api/v1/query_range"):
			sawRange = true
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	})
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "find_error_pattern_logs", map[string]any{
		"org": "acme", "service": "api",
	})
	if sawRange {
		t.Error("query_range hit despite size guard — refusal didn't take effect")
	}
	if !strings.Contains(resultText(res), "estimated_size_too_large") {
		t.Errorf("response should signal size refusal: %s", resultText(res))
	}
}

func TestHandler_FindSlowRequests(t *testing.T) {
	var sawQuery string
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/search") {
			sawQuery = r.URL.Query().Get("q")
			_, _ = w.Write([]byte(`{"traces":[]}`))
			return
		}
		http.NotFound(w, r)
	})
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "find_slow_requests", map[string]any{
		"org": "acme", "service": "checkout", "min_duration": "750ms", "errors_only": true,
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	wantSubs := []string{`resource.service.name = "checkout"`, "duration > 750ms", "status = error"}
	for _, want := range wantSubs {
		if !strings.Contains(sawQuery, want) {
			t.Errorf("traceql %q missing %q", sawQuery, want)
		}
	}
}

