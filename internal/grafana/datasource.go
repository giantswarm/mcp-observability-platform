package grafana

import "strings"

// Datasource is the domain projection of a Grafana datasource. The
// GrafanaOrganization CR carries {ID, Name} only; the full record
// (URL, secureJsonData, etc.) lives behind LookupDatasourceUIDByID.
type Datasource struct {
	ID   int64
	Name string
}

// DatasourceKind names the canonical role a datasource plays for the
// MCP (metrics backend, logs backend, traces backend, alerting).
//
// MatchKind picks the concrete Datasource by case-insensitive name
// substring; the kind ↔ substring rules live here so the substring
// vocabulary stays in one place. The roadmap's "Datasource UID + kind
// in CR status" item makes this matching obsolete by reading kind off
// the CR directly.
type DatasourceKind string

const (
	DSKindMimir        DatasourceKind = "mimir"
	DSKindLoki         DatasourceKind = "loki"
	DSKindTempo        DatasourceKind = "tempo"
	DSKindAlertmanager DatasourceKind = "alertmanager"
)

// datasourceKindSubstring is the single source of truth for "what
// substring identifies a datasource of kind K?". Kept private so
// changing it doesn't ripple to consumers — they reference the kind
// constants.
var datasourceKindSubstring = map[DatasourceKind]string{
	DSKindMimir:        "mimir",
	DSKindLoki:         "loki",
	DSKindTempo:        "tempo",
	DSKindAlertmanager: "alertmanager",
}

// MatchKind returns the first datasource whose name contains the
// kind's canonical substring (case-insensitive).
func MatchKind(dss []Datasource, kind DatasourceKind) (Datasource, bool) {
	needle, ok := datasourceKindSubstring[kind]
	if !ok {
		return Datasource{}, false
	}
	for _, ds := range dss {
		if strings.Contains(strings.ToLower(ds.Name), needle) {
			return ds, true
		}
	}
	return Datasource{}, false
}
