package grafana

import "strings"

// Datasource is the domain projection of a Grafana datasource.
type Datasource struct {
	ID           int64
	Name         string
	UID          string
	Type         string
	ManageAlerts bool
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

// DSType* are Grafana datasource plugin-type strings (the `type`
// field on /api/datasources entries). Distinct from DSKind* which
// is a name-substring matcher: a Mimir datasource registers as
// plugin type "prometheus", not "mimir".
const (
	DSTypePrometheus = "prometheus"
	DSTypeLoki       = "loki"
	DSTypeTempo      = "tempo"
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
