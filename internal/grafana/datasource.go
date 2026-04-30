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

// DatasourceType is the Grafana datasource plugin-type string (the
// `type` field on /api/datasources entries). A Mimir datasource
// registers as DSTypePrometheus — kind→type alone can't distinguish
// Mimir from a vanilla Prometheus DS. If an org carries both, the
// caller must disambiguate with an explicit datasource_uid.
type DatasourceType string

const (
	DSTypePrometheus   DatasourceType = "prometheus"
	DSTypeLoki         DatasourceType = "loki"
	DSTypeTempo        DatasourceType = "tempo"
	DSTypeAlertmanager DatasourceType = "alertmanager"
)

// MatchesType reports whether ds's plugin type contains dsType
// (case-insensitive).
func MatchesType(ds Datasource, dsType DatasourceType) bool {
	return strings.Contains(strings.ToLower(ds.Type), strings.ToLower(string(dsType)))
}

// FilterDatasourcesByType returns every ds whose plugin type contains
// dsType, in input order. Used by the binder to pick the default
// datasource (first match) and to validate caller-supplied UIDs.
func FilterDatasourcesByType(dss []Datasource, dsType DatasourceType) []Datasource {
	out := make([]Datasource, 0, len(dss))
	for _, ds := range dss {
		if MatchesType(ds, dsType) {
			out = append(out, ds)
		}
	}
	return out
}
