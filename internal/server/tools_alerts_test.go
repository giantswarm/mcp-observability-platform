package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSeverityRank(t *testing.T) {
	cases := map[string]int{
		"critical": 4,
		"CRITICAL": 4,
		"page":     4,
		"error":    3,
		"high":     3,
		"warning":  2,
		"warn":     2,
		"info":     1,
		"notice":   1,
		"low":      1,
		"":         0,
		"unknown":  0,
	}
	for in, want := range cases {
		if got := severityRank(in); got != want {
			t.Errorf("severityRank(%q) = %d, want %d", in, got, want)
		}
	}
}

func buildAlerts(specs []struct {
	Name, Sev, State, Fp, Start string
}) json.RawMessage {
	var alerts []amAlert
	for _, s := range specs {
		a := amAlert{
			Fingerprint: s.Fp,
			StartsAt:    s.Start,
			Labels:      map[string]string{"alertname": s.Name, "severity": s.Sev},
		}
		a.Status.State = s.State
		alerts = append(alerts, a)
	}
	b, _ := json.Marshal(alerts)
	return b
}

func TestPaginateAlerts_SortsBySeverityThenStart(t *testing.T) {
	raw := buildAlerts([]struct{ Name, Sev, State, Fp, Start string }{
		{"A", "warning", "active", "f1", "2026-04-18T10:00:00Z"},
		{"B", "critical", "active", "f2", "2026-04-18T12:00:00Z"},
		{"C", "critical", "active", "f3", "2026-04-18T11:00:00Z"},
		{"D", "info", "active", "f4", "2026-04-18T09:00:00Z"},
	})
	res, err := paginateAlerts(raw, 0, 50)
	if err != nil {
		t.Fatalf("paginateAlerts: %v", err)
	}
	// Decode back to inspect ordering
	j, _ := json.Marshal(res)
	var out struct {
		Items []struct {
			Name, Fingerprint, Severity string
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(j, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 4 {
		t.Fatalf("total = %d, want 4", out.Total)
	}
	got := []string{out.Items[0].Fingerprint, out.Items[1].Fingerprint, out.Items[2].Fingerprint, out.Items[3].Fingerprint}
	want := []string{"f3", "f2", "f1", "f4"} // critical (earlier first), warning, info
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestPaginateAlerts_PageBoundaries(t *testing.T) {
	specs := []struct{ Name, Sev, State, Fp, Start string }{}
	for i := 0; i < 5; i++ {
		specs = append(specs, struct{ Name, Sev, State, Fp, Start string }{
			Name:  "A",
			Sev:   "warning",
			State: "active",
			Fp:    fmt.Sprintf("fp-%02d", i),
			Start: fmt.Sprintf("2026-04-18T10:%02d:00Z", i),
		})
	}
	raw := buildAlerts(specs)
	p0, _ := paginateAlerts(raw, 0, 2)
	p1, _ := paginateAlerts(raw, 1, 2)
	p2, _ := paginateAlerts(raw, 2, 2)
	p3, _ := paginateAlerts(raw, 3, 2) // past end

	checkPage := func(name string, res any, wantLen int, wantHasMore bool) {
		b, _ := json.Marshal(res)
		var out struct {
			Items   []any `json:"items"`
			HasMore bool  `json:"hasMore"`
		}
		_ = json.Unmarshal(b, &out)
		if len(out.Items) != wantLen {
			t.Errorf("%s items = %d, want %d", name, len(out.Items), wantLen)
		}
		if out.HasMore != wantHasMore {
			t.Errorf("%s hasMore = %v, want %v", name, out.HasMore, wantHasMore)
		}
	}
	checkPage("p0", p0, 2, true)
	checkPage("p1", p1, 2, true)
	checkPage("p2", p2, 1, false)
	checkPage("p3", p3, 0, false)
}

func TestPaginateAlerts_UnnamedFallback(t *testing.T) {
	raw := json.RawMessage(`[{"labels":{"severity":"warning"},"status":{"state":"active"},"fingerprint":"f1"}]`)
	res, err := paginateAlerts(raw, 0, 10)
	if err != nil {
		t.Fatalf("paginateAlerts: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"name":"(unnamed)"`) {
		t.Errorf("expected (unnamed) fallback, got %s", b)
	}
}

func TestFindAlertByFingerprint(t *testing.T) {
	raw := buildAlerts([]struct{ Name, Sev, State, Fp, Start string }{
		{"A", "warning", "active", "aaa", ""},
		{"B", "critical", "silenced", "bbb", ""},
	})
	a, err := findAlertByFingerprint(raw, "bbb")
	if err != nil {
		t.Fatalf("findAlertByFingerprint: %v", err)
	}
	if a == nil || a.Fingerprint != "bbb" || a.Labels["alertname"] != "B" {
		t.Errorf("got %+v, want fingerprint=bbb alertname=B", a)
	}
	miss, err := findAlertByFingerprint(raw, "nope")
	if err != nil || miss != nil {
		t.Errorf("expected (nil, nil) for miss, got (%v, %v)", miss, err)
	}
}
