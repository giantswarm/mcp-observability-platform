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
// MCP (metrics backend, logs backend, traces backend, alerting). The
// const value doubles as the case-insensitive substring MatchKind
// looks for in Datasource.Name.
type DatasourceKind string

const (
	DSKindMimir        DatasourceKind = "mimir"
	DSKindLoki         DatasourceKind = "loki"
	DSKindTempo        DatasourceKind = "tempo"
	DSKindAlertmanager DatasourceKind = "alertmanager"
)

// MatchKind returns the first datasource whose name contains the
// kind's canonical substring (case-insensitive).
func MatchKind(dss []Datasource, kind DatasourceKind) (Datasource, bool) {
	if kind == "" {
		return Datasource{}, false
	}
	needle := strings.ToLower(string(kind))
	for _, ds := range dss {
		if strings.Contains(strings.ToLower(ds.Name), needle) {
			return ds, true
		}
	}
	return Datasource{}, false
}
