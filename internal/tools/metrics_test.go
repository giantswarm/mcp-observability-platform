package tools

import (
	"encoding/json"
	"testing"
)

const promRulesJSON = `{
  "status":"success",
  "data":{"groups":[
    {"name":"grp1","file":"/etc/g1.yaml","rules":[
      {"type":"alerting","name":"HighErrorRate","query":"rate(err[5m]) > 0.1","state":"firing","health":"ok","labels":{"severity":"critical"},"annotations":{"summary":"bad"}},
      {"type":"alerting","name":"DiskWarning","query":"disk > 0.8","state":"inactive","health":"ok","labels":{"severity":"warning"}},
      {"type":"recording","name":"job:rate","query":"sum by (job) (rate(http[1m]))","state":"","health":"ok"}
    ]},
    {"name":"grp2","file":"/etc/g2.yaml","rules":[
      {"type":"alerting","name":"LatencyHigh","query":"p95 > 2","state":"pending","health":"ok","labels":{"severity":"critical"}}
    ]}
  ]}
}`

func TestFlattenAlertRules_FiltersAndSorts(t *testing.T) {
	got, err := flattenAlertRules(json.RawMessage(promRulesJSON), "alert", "all", "")
	if err != nil {
		t.Fatalf("flattenAlertRules: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 alert rules, got %d", len(got))
	}
	// Sorted by type then name (all alerts here, so just alpha-by-name).
	wantOrder := []string{"DiskWarning", "HighErrorRate", "LatencyHigh"}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Errorf("order[%d] = %s, want %s", i, got[i].Name, name)
		}
	}
	// State must be preserved.
	if got[1].State != "firing" {
		t.Errorf("HighErrorRate state = %q, want firing", got[1].State)
	}
}

func TestFlattenAlertRules_FilterByState(t *testing.T) {
	got, err := flattenAlertRules(json.RawMessage(promRulesJSON), "alert", "firing", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].Name != "HighErrorRate" {
		t.Errorf("state=firing got %+v", got)
	}
}

func TestFlattenAlertRules_FilterByType(t *testing.T) {
	got, _ := flattenAlertRules(json.RawMessage(promRulesJSON), "record", "all", "")
	if len(got) != 1 || got[0].Type != "record" || got[0].Name != "job:rate" {
		t.Errorf("type=record got %+v", got)
	}
}

func TestFlattenAlertRules_FilterByName(t *testing.T) {
	got, _ := flattenAlertRules(json.RawMessage(promRulesJSON), "alert", "all", "disk")
	if len(got) != 1 || got[0].Name != "DiskWarning" {
		t.Errorf("nameContains=disk got %+v", got)
	}
}
