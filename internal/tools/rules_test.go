package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleLokiRulesJSON = `{
  "status":"success",
  "data":{
    "groups":[
      {
        "name":"alerts",
        "file":"alerts.yaml",
        "rules":[
          {"type":"alerting","name":"HighErrorRate","query":"sum(rate(...))","state":"firing","health":"ok",
           "labels":{"severity":"critical"},"annotations":{"summary":"errors high"}}
        ]
      },
      {
        "name":"recordings",
        "file":"recordings.yaml",
        "rules":[
          {"type":"recording","name":"job:loglines:rate5m","query":"rate(...)","health":"ok"}
        ]
      }
    ]
  }
}`

func TestProjectLokiRules_All(t *testing.T) {
	got, err := projectLokiRules(json.RawMessage(sampleLokiRulesJSON), filterAll)
	if err != nil {
		t.Fatalf("projectLokiRules: %v", err)
	}
	body, _ := json.Marshal(got)
	if !strings.Contains(string(body), `"total":2`) {
		t.Errorf("want total=2, got: %s", body)
	}
	if !strings.Contains(string(body), `"HighErrorRate"`) || !strings.Contains(string(body), `"job:loglines:rate5m"`) {
		t.Errorf("expected both rules in output: %s", body)
	}
}

func TestProjectLokiRules_FilterRecording(t *testing.T) {
	got, err := projectLokiRules(json.RawMessage(sampleLokiRulesJSON), "recording")
	if err != nil {
		t.Fatalf("projectLokiRules: %v", err)
	}
	body, _ := json.Marshal(got)
	if !strings.Contains(string(body), `"total":1`) {
		t.Errorf("want total=1, got: %s", body)
	}
	if strings.Contains(string(body), `"HighErrorRate"`) {
		t.Errorf("alerting rule should be filtered out: %s", body)
	}
}

func TestProjectLokiRules_RejectsUnknownType(t *testing.T) {
	_, err := projectLokiRules(json.RawMessage(sampleLokiRulesJSON), "bogus")
	if err == nil {
		t.Fatal("want error for unknown type filter")
	}
}

func TestProjectLokiRules_BadJSON(t *testing.T) {
	_, err := projectLokiRules(json.RawMessage(`{not json}`), filterAll)
	if err == nil {
		t.Fatal("want unmarshal error")
	}
}
