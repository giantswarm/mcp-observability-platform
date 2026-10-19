# The tools

Generated from the registered tools (`make tools-doc`), none disabled by `--disabled-tools`. The descriptions and schemas of the tools delegated to grafana/mcp-grafana are upstream's, led by a summary line of ours where upstream's first line is longer than muster shows; the binder adds the `org` argument and, on the datasource-bound tools, replaces the `datasourceUid` description.

| Tool | Kind |
|---|---|
| `alerting_rules_read` | read-only |
| `diff_tempo_traces` | read-only |
| `generate_deeplink` | write |
| `get_alert` | read-only |
| `get_dashboard_by_uid` | read-only |
| `get_dashboard_panel_queries` | read-only |
| `get_dashboard_property` | read-only |
| `get_dashboard_summary` | read-only |
| `get_datasource` | read-only |
| `get_panel_image` | read-only |
| `get_query_examples` | read-only |
| `get_silence` | read-only |
| `get_tempo_trace` | read-only |
| `get_tempo_traceql_docs` | read-only |
| `list_alerts` | read-only |
| `list_datasources` | read-only |
| `list_loki_label_names` | read-only |
| `list_loki_label_values` | read-only |
| `list_orgs` | read-only |
| `list_prometheus_label_names` | read-only |
| `list_prometheus_label_values` | read-only |
| `list_prometheus_metric_metadata` | read-only |
| `list_prometheus_metric_names` | read-only |
| `list_silences` | read-only |
| `list_tempo_attribute_names` | read-only |
| `list_tempo_attribute_values` | read-only |
| `query_loki_logs` | read-only |
| `query_loki_patterns` | read-only |
| `query_loki_stats` | read-only |
| `query_prometheus` | read-only |
| `query_prometheus_histogram` | read-only |
| `query_tempo_metrics` | read-only |
| `run_panel_query` | read-only |
| `search_dashboards` | read-only |
| `search_folders` | read-only |
| `search_tempo_traces` | read-only |

## `alerting_rules_read`

```text
List and inspect Grafana alert rules with filtering capabilities.

When to use:
- Understanding why an alert is or isn't firing
- Auditing alert rule configuration (queries, conditions, labels, notification settings)
- Finding alert rules by state, folder, group, or name
- Comparing rule versions to see what changed

Read-only: does not create, update, or delete rules, and does not show notification routing.
```

```json
{
  "properties": {
    "datasource_uid": {
      "description": "Optional: UID of a Prometheus or Loki datasource to query for datasource-managed alert rules. If omitted, returns Grafana-managed rules.",
      "type": "string"
    },
    "folder_uid": {
      "description": "Filter by exact folder UID (for 'list' operation). Mutually exclusive with search_folder.",
      "type": "string"
    },
    "label_selectors": {
      "description": "Prometheus-style selectors to filter alert rules by labels. Each string is a selector e.g. '{severity=\"critical\", team=~\"backend.*\"}'. All selectors must match (AND).",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "limit_alerts": {
      "description": "Limit alert instances per rule. For list: 0 omits alerts. For get: <=0 defaults to 200. Max 200.",
      "type": "integer"
    },
    "matchers": {
      "description": "Label matchers to filter alert instances. Each string is a Prometheus-style matcher e.g. 'severity=\"critical\"', 'env!=\"dev\"', 'team=~\"backend.*\"'. Requires Grafana 12.4+.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "operation": {
      "description": "The operation to perform: 'list' to search/filter rules, 'get' to retrieve full rule details (state + configuration) by UID, or 'versions' to get the version history of a rule",
      "enum": [
        "list",
        "get",
        "versions"
      ],
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "rule_group": {
      "description": "Filter by exact rule group name (for 'list' operation)",
      "type": "string"
    },
    "rule_limit": {
      "default": 200,
      "description": "Maximum number of rules to return (default 200, max 200). Requires Grafana 12.4+ (for 'list' operation)",
      "type": "integer"
    },
    "rule_type": {
      "description": "Filter by rule type (for 'list' operation)",
      "enum": [
        "alerting",
        "recording"
      ],
      "type": "string"
    },
    "rule_uid": {
      "description": "The UID of the alert rule (required for 'get' and 'versions' operations)",
      "type": "string"
    },
    "search_folder": {
      "description": "Search folders by path using partial matching (for 'list' operation). Requires Grafana 12.4+. Mutually exclusive with folder_uid.",
      "type": "string"
    },
    "search_rule_name": {
      "description": "Search alert rule names/titles using partial matching. Requires Grafana 12.4+ (for 'list' operation)",
      "type": "string"
    },
    "states": {
      "description": "Filter by alert state: firing, pending, normal, recovering, nodata, error (for 'list' operation)",
      "items": {
        "type": "string"
      },
      "type": "array"
    }
  },
  "required": [
    "org",
    "operation"
  ],
  "type": "object"
}
```

## `diff_tempo_traces`

```text
Compare two Tempo traces: a compact summary of what differs, plus the span-level patch when it is small.

Compare two complete traces. Returns a compact summary and includes the full span-level patch when it is at most 64 KiB. Request trace-patch-v0 only when full details are required; full patches are not size-bounded.
```

```json
{
  "properties": {
    "base_end": {
      "description": "Optional end of the baseline trace search range in RFC3339 format. Must be provided with base_start.",
      "type": "string"
    },
    "base_start": {
      "description": "Optional start of the baseline trace search range in RFC3339 format. Must be provided with base_end.",
      "type": "string"
    },
    "base_trace_id": {
      "description": "Trace ID for the baseline trace",
      "type": "string"
    },
    "compare_end": {
      "description": "Optional end of the comparison trace search range in RFC3339 format. Must be provided with compare_start.",
      "type": "string"
    },
    "compare_start": {
      "description": "Optional start of the comparison trace search range in RFC3339 format. Must be provided with compare_end.",
      "type": "string"
    },
    "compare_trace_id": {
      "description": "Trace ID for the comparison trace",
      "type": "string"
    },
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "format": {
      "default": "trace-summary-v0-composed",
      "description": "Output format. The composed format returns a compact summary and attaches the full patch only when it is at most 64 KiB. trace-patch-v0 has no output-size guarantee.",
      "enum": [
        "trace-summary-v0-composed",
        "trace-summary-v0-native",
        "trace-patch-v0"
      ],
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    }
  },
  "required": [
    "org",
    "base_trace_id",
    "compare_trace_id"
  ],
  "type": "object"
}
```

## `generate_deeplink`

```text
Build a Grafana link to a dashboard, panel or Explore query, optionally as a short URL.

Generate deeplink URLs for Grafana resources. Supports dashboards (requires dashboardUid or provisioningPreview), panels (requires dashboardUid or provisioningPreview, plus panelId), and Explore queries (requires datasourceUid and optionally queries). For dashboard and panel links, provisioningPreview points at a dashboard staged on a provisioning repository branch (e.g. a git-sync PR preview). For explore links, the time range and queries are embedded inside the Grafana explore state. Set shorten=true to also attempt a /goto/<uid> short URL; if shortening fails, the full deeplink is returned.
```

```json
{
  "properties": {
    "dashboardUid": {
      "description": "Dashboard UID (for stored dashboards). Mutually exclusive with provisioningPreview for dashboard and panel types.",
      "type": "string"
    },
    "datasourceUid": {
      "description": "Datasource UID (required for explore type)",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "panelId": {
      "description": "Panel ID (required for panel type)",
      "type": "integer"
    },
    "provisioningPreview": {
      "description": "Identifies a dashboard staged on a provisioning repository branch (e.g. a git-sync PR preview). Mutually exclusive with dashboardUid for dashboard and panel types.",
      "properties": {
        "path": {
          "description": "Path to the dashboard file within the repository, relative to its root.",
          "type": "string"
        },
        "pullRequestUrl": {
          "description": "Upstream pull request URL to surface in Grafana's preview banner.",
          "type": "string"
        },
        "ref": {
          "description": "Branch or commit SHA. Defaults to the repository's main branch when omitted.",
          "type": "string"
        },
        "repo": {
          "description": "Provisioning repository slug (the repository name).",
          "type": "string"
        }
      },
      "required": [
        "repo",
        "path"
      ],
      "type": "object"
    },
    "queries": {
      "description": "List of query objects for explore links (e.g. [{\"refId\":\"A\",\"expr\":\"up\"}])",
      "items": {
        "type": "object"
      },
      "type": "array"
    },
    "queryParams": {
      "additionalProperties": {
        "type": "string"
      },
      "description": "Additional URL query parameters (for dashboard/panel types)",
      "type": "object"
    },
    "resourceType": {
      "description": "Type of resource: dashboard, panel, or explore",
      "type": "string"
    },
    "shorten": {
      "description": "If true, try to shorten the generated URL to /goto/<uid>. If shortening fails, return the original deeplink.",
      "type": "boolean"
    },
    "timeRange": {
      "description": "Time range for the link",
      "properties": {
        "from": {
          "description": "Start time (e.g., 'now-1h')",
          "type": "string"
        },
        "to": {
          "description": "End time (e.g., 'now')",
          "type": "string"
        }
      },
      "type": "object"
    }
  },
  "required": [
    "org",
    "resourceType"
  ],
  "type": "object"
}
```

## `get_alert`

```text
Get one Alertmanager alert by its fingerprint from list_alerts: labels, annotations, times, silencedBy, inhibitedBy.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "fingerprint": {
      "description": "Alertmanager fingerprint (from list_alerts.items[].fingerprint).",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    }
  },
  "required": [
    "org",
    "fingerprint"
  ],
  "type": "object"
}
```

## `get_dashboard_by_uid`

```text
Get a whole Grafana dashboard by UID, or a saved version: panels, variables, settings. Can be very large.

Retrieves the complete dashboard, including panels, variables, and settings, for a specific dashboard identified by its UID. Pass optional version to load a saved snapshot instead of the current dashboard. Saved snapshots are fetched through Grafana's legacy versions API, so 'apiVersion' is omitted and 'meta' is partial: only version, createdBy, and created are populated. For current (non-versioned) fetches, the response includes 'apiVersion' and 'isV2': when 'isV2' is true the dashboard uses the v2 schema (panels live under 'elements' keyed by name, arranged by 'layout'; variables under 'variables'), otherwise it is classic v1 ('panels[]' with 'templating.list'). The full JSON of a large dashboard can be very large.
```

```json
{
  "properties": {
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "uid": {
      "description": "The UID of the dashboard",
      "type": "string"
    },
    "version": {
      "description": "Optional saved version. Omit for the current dashboard",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "org",
    "uid"
  ],
  "type": "object"
}
```

## `get_dashboard_panel_queries`

```text
Get the queries of a Grafana dashboard's panels, or of one panel, optionally with variables substituted.

Retrieve panel queries from a Grafana dashboard. Supports all datasource types (Prometheus, Loki, CloudWatch, SQL, etc.) and row-nested panels. Optionally filter to a specific panel by ID with `panelId`. Optionally provide `variables` for template variable substitution, which populates `processedQuery` and `requiredVariables` fields. Returns an array of objects with fields: title, query (raw expression), datasource (object with uid and type), and optionally processedQuery, refId, requiredVariables, and target. Targets built in a visual editor (CloudWatch metric search, InfluxDB query builder) have no string expression: those return an empty query plus `target`, the panel's raw query JSON. They can be executed as panel queries but not as standalone expressions.
```

```json
{
  "properties": {
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "panelId": {
      "description": "Optional panel ID to filter to a specific panel",
      "type": "integer"
    },
    "uid": {
      "description": "The UID of the dashboard",
      "type": "string"
    },
    "variables": {
      "additionalProperties": {
        "type": "string"
      },
      "description": "Optional variable substitutions (e.g., {\"job\": \"api-server\"})",
      "type": "object"
    }
  },
  "required": [
    "org",
    "uid"
  ],
  "type": "object"
}
```

## `get_dashboard_property`

```text
Get parts of a Grafana dashboard by JSONPath, e.g. panel titles or queries, without the whole JSON.

Get specific parts of a dashboard using JSONPath expressions to minimize context window usage. JSONPath targets the dashboard's native schema. Classic v1 paths: '$.title' (title), '$.panels[*].title' (all panel titles), '$.panels[0]' (first panel), '$.templating.list' (variables), '$.annotations.list' (saved dashboard annotation queries/definitions), '$.tags' (tags), '$.panels[*].targets[*].expr' (all queries). v2 dashboards (isV2 true) use different paths: '$.title', '$.elements' (panels, keyed by name), '$.variables' (variables), '$.annotations'.
```

```json
{
  "properties": {
    "jsonPath": {
      "description": "JSONPath expression to extract specific data (e.g., '$.panels[0].title' for first panel title, '$.panels[*].title' for all panel titles, '$.templating.list' for variables, '$.annotations.list' for saved dashboard annotation queries/definitions)",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "uid": {
      "description": "The UID of the dashboard",
      "type": "string"
    }
  },
  "required": [
    "org",
    "uid",
    "jsonPath"
  ],
  "type": "object"
}
```

## `get_dashboard_summary`

```text
Get a compact overview of a Grafana dashboard: title, panel count and types, variables; not the full JSON.

Get a compact summary of a dashboard including title, panel count, panel types, variables, and other metadata without the full JSON. Use this for dashboard overview and planning modifications without consuming large context windows.
```

```json
{
  "properties": {
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "uid": {
      "description": "The UID of the dashboard",
      "type": "string"
    }
  },
  "required": [
    "org",
    "uid"
  ],
  "type": "object"
}
```

## `get_datasource`

```text
Get one Grafana datasource by UID or name: type, URL, access settings and JSON data.

Retrieves detailed information about a specific datasource by UID or name. Returns the full datasource model, including name, type, URL, access settings, JSON data, and secure JSON field status. Provide either uid or name; uid takes priority if both are given.
```

```json
{
  "properties": {
    "name": {
      "description": "The name of the datasource. Used if UID is not provided.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "uid": {
      "description": "The UID of the datasource. If provided, takes priority over name.",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `get_panel_image`

```text
Render a Grafana panel, dashboard or Explore view as a PNG image; needs the Grafana Image Renderer.

Render a Grafana dashboard panel, full dashboard or Explore view as a PNG image. Returns the image as base64 encoded data. Requires the Grafana Image Renderer service to be installed. Exactly one of dashboardUid (for stored dashboards), provisioningPreview (for dashboards staged on a provisioning repository branch, e.g. a git-sync PR) or explore (an ad-hoc datasource query rendered in Explore, no saved dashboard needed) must be supplied. Use this for generating visual snapshots of dashboards for reports, alerts, or presentations.
```

```json
{
  "properties": {
    "dashboardUid": {
      "description": "The UID of a stored dashboard containing the panel. Required unless provisioningPreview is provided.",
      "type": "string"
    },
    "explore": {
      "description": "Render an ad-hoc Explore view for a datasource and queries instead of a stored dashboard. Mutually exclusive with dashboardUid and provisioningPreview. Requires Grafana 10.2 or later.",
      "properties": {
        "datasourceUid": {
          "description": "Datasource UID to query",
          "type": "string"
        },
        "queries": {
          "description": "List of query objects (e.g. [{\"refId\":\"A\", \"expr\":\"up\"}])",
          "items": {
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "datasourceUid"
      ],
      "type": "object"
    },
    "height": {
      "description": "Height of the rendered image in pixels. Defaults to 500 (1000 for explore)",
      "type": "integer"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "panelId": {
      "description": "The ID of the panel to render. If omitted, the entire dashboard is rendered",
      "type": "integer"
    },
    "provisioningPreview": {
      "description": "Render a dashboard from a provisioning repository branch (e.g. a git-sync PR preview). Mutually exclusive with dashboardUid.",
      "properties": {
        "path": {
          "description": "Path to the dashboard file within the repository, relative to its root.",
          "type": "string"
        },
        "ref": {
          "description": "Branch or commit SHA to render from. Defaults to the repository's main branch when omitted.",
          "type": "string"
        },
        "repo": {
          "description": "Provisioning repository slug. List repositories via /apis/provisioning.grafana.app/v0alpha1/namespaces/default/repositories if unknown.",
          "type": "string"
        }
      },
      "required": [
        "repo",
        "path"
      ],
      "type": "object"
    },
    "scale": {
      "description": "Scale factor for the image (1-3). Defaults to 1",
      "type": "integer"
    },
    "theme": {
      "description": "Theme for the rendered image: light or dark. Defaults to dark",
      "type": "string"
    },
    "timeRange": {
      "description": "Time range for the rendered image",
      "properties": {
        "from": {
          "description": "Start time (e.g., 'now-1h', '2024-01-01T00:00:00Z')",
          "type": "string"
        },
        "to": {
          "description": "End time (e.g., 'now', '2024-01-01T12:00:00Z')",
          "type": "string"
        }
      },
      "type": "object"
    },
    "timeout": {
      "description": "Rendering timeout in seconds. Defaults to 60",
      "type": "integer"
    },
    "variables": {
      "additionalProperties": {
        "oneOf": [
          {
            "type": "string"
          },
          {
            "items": {
              "type": "string"
            },
            "type": "array"
          }
        ]
      },
      "description": "Dashboard variables to apply. Values can be a single string or an array of strings for multi-value variables (e.g., {\"var-datasource\": \"prometheus\", \"var-instance\": [\"server1\", \"server2\"]})",
      "type": "object"
    },
    "width": {
      "description": "Width of the rendered image in pixels. Defaults to 1000",
      "type": "integer"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `get_query_examples`

```text
Get example queries for a datasource type: PromQL, LogQL, ClickHouse SQL, CloudWatch, InfluxDB.

Get example queries for a specific datasource type. Provides sample queries with descriptions for Prometheus (PromQL), Loki (LogQL), ClickHouse (SQL with Grafana macros), CloudWatch (metric configurations), and InfluxDB (Flux and InfluxQL). Use this to understand query syntax and common patterns for each datasource.
```

```json
{
  "properties": {
    "datasourceType": {
      "description": "The datasource type to get examples for (e.g. 'prometheus', 'loki', 'clickhouse', 'cloudwatch', 'influxdb')",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    }
  },
  "required": [
    "org",
    "datasourceType"
  ],
  "type": "object"
}
```

## `get_silence`

```text
Get one Alertmanager silence by id, e.g. from get_alert's silencedBy: matchers, times, author, comment, state.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "id": {
      "description": "Alertmanager silence id (UUID).",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    }
  },
  "required": [
    "org",
    "id"
  ],
  "type": "object"
}
```

## `get_tempo_trace`

```text
Retrieve a specific trace by ID
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "focus_span_id": {
      "description": "Optional 16-character hexadecimal span ID to select initially. The full trace remains available.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "trace_id": {
      "description": "Trace ID to retrieve",
      "type": "string"
    }
  },
  "required": [
    "org",
    "trace_id"
  ],
  "type": "object"
}
```

## `get_tempo_traceql_docs`

```text
Get the TraceQL reference with examples; read it before a non-trivial TraceQL query or after one fails.

Retrieve TraceQL reference documentation with examples, covering attribute filters, aggregates, structural operators, and metrics queries. Consult this before writing a non-trivial TraceQL query, or after one returns an error or no results.
```

```json
{
  "properties": {
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "topic": {
      "description": "Which section of the TraceQL reference to retrieve: 'basic' for attribute and duration filters, 'aggregates' for count/avg/sum and by() grouping, 'structural' for parent/child and descendant operators, 'metrics' for rate/quantile_over_time metrics queries",
      "enum": [
        "basic",
        "aggregates",
        "structural",
        "metrics"
      ],
      "type": "string"
    }
  },
  "required": [
    "org",
    "topic"
  ],
  "type": "object"
}
```

## `list_alerts`

```text
List the org's Alertmanager alerts, most severe first, paged; get_alert has one alert's full detail.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "filter": {
      "description": "Alertmanager label matcher, e.g. 'alertname=~\"Kube.*\"' or 'severity=\"critical\"'",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "page": {
      "description": "0-based page index (default 0)",
      "type": "number"
    },
    "pageSize": {
      "description": "Page size (default 50, max 500)",
      "type": "number"
    },
    "state": {
      "description": "'active' (default) | 'silenced' | 'inhibited' | 'all'",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_datasources`

```text
List the org's Grafana datasources with their UIDs, optionally filtered by type or name, paged.

List all configured datasources in Grafana. Use this to discover available datasources and their UIDs. Supports filtering by type and/or name (case-insensitive substring match) and pagination.
```

```json
{
  "properties": {
    "limit": {
      "default": 50,
      "description": "Maximum number of datasources to return (max 100)",
      "type": "integer"
    },
    "name": {
      "description": "Case-insensitive substring match on the datasource name. For example, 'prod' matches datasources named 'prometheus-prod-eu' and 'loki-prod-us'. Useful on instances with many datasources where filtering by type alone returns too many results.",
      "type": "string"
    },
    "offset": {
      "default": 0,
      "description": "Number of datasources to skip for pagination",
      "type": "integer"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "type": {
      "description": "The type of datasources to search for. For example, 'prometheus', 'loki', 'tempo', etc...",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_loki_label_names`

```text
List the label names in the org's Loki logs over a time range (default the last hour).

Lists all available label/field names (keys) found in logs within a specified Loki or VictoriaLogs datasource and time range. Returns a list of unique label strings (e.g., `["app", "env", "pod"]`). If the time range is not provided, it defaults to the last hour. Optionally narrow the search to a subset of streams with `matcher` (e.g. `{namespace="prod"}`).
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the query in RFC3339 format or relative time (e.g. 'now') (defaults to now)",
      "type": "string"
    },
    "matcher": {
      "description": "Optionally, a stream selector to narrow the search to matching streams (Loki: LogQL, e.g. '{namespace=\"prod\"}'; VictoriaLogs: LogsQL). Defaults to searching across all streams.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the query in RFC3339 format or relative time (e.g. 'now-1h') (defaults to 1 hour ago)",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_loki_label_values`

```text
List the values of one Loki log label over a time range (default the last hour), optionally per stream selector.

Retrieves all unique values associated with a specific `labelName` within a Loki or VictoriaLogs datasource and time range. Returns a list of string values (e.g., for `labelName="env"`, might return `["prod", "staging", "dev"]`). Useful for discovering filter options. Defaults to the last hour if the time range is omitted. Optionally narrow the search to a subset of streams with `matcher` (e.g. `{namespace="prod"}`) — for example, to list `service` values seen only within a specific namespace.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the query in RFC3339 format or relative time (e.g. 'now') (defaults to now)",
      "type": "string"
    },
    "labelName": {
      "description": "The name of the label to retrieve values for (e.g. 'app', 'env', 'pod')",
      "type": "string"
    },
    "matcher": {
      "description": "Optionally, a stream selector to narrow the search to matching streams (Loki: LogQL, e.g. '{namespace=\"prod\"}'; VictoriaLogs: LogsQL). Defaults to searching across all streams.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the query in RFC3339 format or relative time (e.g. 'now-1h') (defaults to 1 hour ago)",
      "type": "string"
    }
  },
  "required": [
    "org",
    "labelName"
  ],
  "type": "object"
}
```

## `list_orgs`

```text
List the Grafana orgs you can access; call it first for the org value every other tool takes.

tenantTypes says which tools serve an org: data for metrics, logs, traces and alert rules; alerting for alerts and silences.
```

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `list_prometheus_label_names`

```text
List the label names in the org's Mimir metrics, optionally for a series selector and time range.

List label names in a PromQL-compatible datasource (Prometheus, Thanos, Mimir, Cloud Monitoring, etc.). Allows filtering by series selectors and time range.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the time range to filter the results by. Supports RFC3339 or relative time (e.g. 'now')",
      "type": "string"
    },
    "limit": {
      "default": 100,
      "description": "Optionally, the maximum number of results to return",
      "type": "integer"
    },
    "matches": {
      "description": "Optionally, a list of label matchers to filter the results by",
      "items": {
        "properties": {
          "filters": {
            "items": {
              "properties": {
                "name": {
                  "description": "The name of the label to match against",
                  "type": "string"
                },
                "type": {
                  "description": "One of the '=' or '!=' or '=~' or '!~'",
                  "type": "string"
                },
                "value": {
                  "description": "The value to match against",
                  "type": "string"
                }
              },
              "required": [
                "name",
                "value",
                "type"
              ],
              "type": "object"
            },
            "type": "array"
          }
        },
        "type": "object"
      },
      "type": "array"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "projectName": {
      "description": "GCP project name to query (Cloud Monitoring datasources only). Overrides or substitutes the defaultProject configured on the datasource.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the time range to filter the results by. Supports RFC3339 or relative time (e.g. 'now-1h')",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_prometheus_label_values`

```text
List the values of one Prometheus label in the org's Mimir metrics, optionally per selector and time range.

Gets the values for a specific label name in a PromQL-compatible datasource (Prometheus, Thanos, Mimir, Cloud Monitoring, etc.). Allows filtering by series selectors and time range.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the query. Supports RFC3339 or relative time (e.g. 'now')",
      "type": "string"
    },
    "labelName": {
      "description": "The name of the label to query",
      "type": "string"
    },
    "limit": {
      "default": 100,
      "description": "Optionally, the maximum number of results to return",
      "type": "integer"
    },
    "matches": {
      "description": "Optionally, a list of selectors to filter the results by",
      "items": {
        "properties": {
          "filters": {
            "items": {
              "properties": {
                "name": {
                  "description": "The name of the label to match against",
                  "type": "string"
                },
                "type": {
                  "description": "One of the '=' or '!=' or '=~' or '!~'",
                  "type": "string"
                },
                "value": {
                  "description": "The value to match against",
                  "type": "string"
                }
              },
              "required": [
                "name",
                "value",
                "type"
              ],
              "type": "object"
            },
            "type": "array"
          }
        },
        "type": "object"
      },
      "type": "array"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "projectName": {
      "description": "GCP project name to query (Cloud Monitoring datasources only). Overrides or substitutes the defaultProject configured on the datasource.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the query. Supports RFC3339 or relative time (e.g. 'now-1h')",
      "type": "string"
    }
  },
  "required": [
    "org",
    "labelName"
  ],
  "type": "object"
}
```

## `list_prometheus_metric_metadata`

```text
List Prometheus metric metadata for the metrics currently scraped from targets.

List Prometheus metric metadata. Returns metadata about metrics currently scraped from targets. Note: This endpoint is experimental.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "limit": {
      "default": 10,
      "description": "The maximum number of metrics to return",
      "type": "integer"
    },
    "limitPerMetric": {
      "description": "The maximum number of metrics to return per metric",
      "type": "integer"
    },
    "metric": {
      "description": "The metric to query",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "projectName": {
      "description": "GCP project name to query (Cloud Monitoring datasources only). Overrides or substitutes the defaultProject configured on the datasource.",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_prometheus_metric_names`

```text
List the metric names in the org's Mimir metrics, optionally by regex and time range, paged.

Lists metric names in a PromQL-compatible datasource (Prometheus, Thanos, Mimir, Cloud Monitoring, etc.). Filters metric names using the provided regex. Supports pagination (page * limit must not exceed 10000) and an optional time range to restrict results to metrics active within that window. Cloud Monitoring ignores the time range.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the time range to filter the results by. Supports RFC3339 or relative time (e.g. 'now')",
      "type": "string"
    },
    "limit": {
      "default": 10,
      "description": "The maximum number of results to return",
      "type": "integer"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "page": {
      "default": 1,
      "description": "The page number to return",
      "type": "integer"
    },
    "projectName": {
      "description": "GCP project name to query (Cloud Monitoring datasources only). Overrides or substitutes the defaultProject configured on the datasource.",
      "type": "string"
    },
    "regex": {
      "description": "The regex to match against the metric names",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the time range to filter the results by. Supports RFC3339 or relative time (e.g. 'now-1h')",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_silences`

```text
List the org's Alertmanager silences, soonest expiring first, paged; get_silence has one silence's full record.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "matcher": {
      "description": "Alertmanager label matcher to filter silences server-side, e.g. 'alertname=~\"Kube.*\"'.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "page": {
      "description": "0-based page index (default 0)",
      "type": "number"
    },
    "pageSize": {
      "description": "Page size (default 50, max 500)",
      "type": "number"
    },
    "state": {
      "description": "'active' (default) | 'pending' | 'expired' | 'all'",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_tempo_attribute_names`

```text
List the attribute names available to TraceQL queries; narrow by scope (resource, span), or the list is large.

List available attribute names for TraceQL queries. Without a scope (resource, span, etc.) the response can be very large.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "scope": {
      "description": "Scope to filter attributes by (span, resource, event, link, instrumentation). Strongly recommended — omitting scope returns all attributes across all scopes which can be very large (100K+ chars).",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `list_tempo_attribute_values`

```text
List the values of one fully scoped Tempo attribute, e.g. resource.service.name.

List values for a fully scoped attribute name (e.g. resource.service.name). Useful for discovering what values exist for a specific attribute.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "filter-query": {
      "description": "Filter query to apply to the attribute values. It can only have one spanset and only &&'ed conditions like { <cond> && <cond> && ... }. This is useful for filtering the values to a specific set of values.",
      "type": "string"
    },
    "name": {
      "description": "The attribute name to get values for (e.g. 'span.http.method', 'resource.service.name')",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    }
  },
  "required": [
    "org",
    "name"
  ],
  "type": "object"
}
```

## `query_loki_logs`

```text
Run a LogQL query on the org's Loki logs: log lines (newest first by default) or metric samples.

Executes a log query against a Loki or VictoriaLogs datasource and returns matching log entries (or metric samples on Loki). Defaults to the last hour, a limit of 10 entries, and 'backward' direction (newest first). The `logql` parameter takes LogQL on Loki and LogsQL on VictoriaLogs (e.g., Loki: `{app="foo"} |= "error"`; VictoriaLogs: `{app="foo"} "error"`). To count matching log lines precisely, use a `count_over_time()` metric query with queryType='instant'. For broad queries that match many lines, set `format` to 'compact' to group results by stream and avoid repeating label metadata on every line. If this server enables the Loki cost guardrail, expensive queries are rejected before execution: query cost is bytes SCANNED, determined only by the stream selector and time range — line filters (|=) and parsers (| json) reduce what is returned, not what is scanned. Use a stream selector with at least one selective positive label matcher (never `{}`, `=~".*"`/`=~".+"`, or negative-only matchers), keep time ranges narrow; rejected queries return rewrite guidance.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "direction": {
      "description": "Optionally, the direction of the query: 'forward' (oldest first) or 'backward' (newest first, default)",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the query in RFC3339 format or relative time (e.g. 'now')",
      "type": "string"
    },
    "format": {
      "description": "Output format for log (streams) queries: 'full' (default) returns every entry with its own label metadata; 'compact' groups lines by stream so each label set is emitted only once, substantially reducing response size for broad queries. Structured metadata keys that are constant within a stream appear once on the stream header; keys that vary across lines appear per-line. Ignored for metric queries.",
      "enum": [
        "full",
        "compact"
      ],
      "type": "string"
    },
    "limit": {
      "default": 10,
      "description": "Optionally, the maximum number of log lines to return (default max: 100, configurable by MCP server).",
      "type": "integer"
    },
    "logql": {
      "description": "The LogQL query to execute against Loki. This can be a simple label matcher or a complex query with filters, parsers, and expressions. Supports full LogQL syntax including label matchers, filter operators, pattern expressions, and pipeline operations.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "queryType": {
      "description": "Query type: 'range' (default) or 'instant'. Instant queries return a single value at one point in time. Range queries return values over a time window. Use 'instant' for metric queries when you want the current value.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the query in RFC3339 format or relative time (e.g. 'now-1h')",
      "type": "string"
    },
    "stepSeconds": {
      "description": "Resolution step in seconds for range metric queries. When running metric queries with queryType='range', this controls the time resolution of the returned data points.",
      "type": "integer"
    }
  },
  "required": [
    "org",
    "logql"
  ],
  "type": "object"
}
```

## `query_loki_patterns`

```text
Get the common log patterns, with counts, for a Loki stream selector over a time range.

Retrieves detected log patterns from a Loki datasource for a given stream selector and time range. Returns a list of patterns, each containing a pattern string and a total count of occurrences. Patterns help identify common log structures and anomalies. The `logql` parameter must be a stream selector (e.g., `{job="nginx"}`) and does not support line filters or aggregations. Defaults to the last hour if the time range is omitted. **Not supported on VictoriaLogs** datasources - use a `| stats` pipeline instead.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the query in RFC3339 format or relative time (e.g. 'now') (defaults to now)",
      "type": "string"
    },
    "logql": {
      "description": "A LogQL stream selector to identify the logs to analyze for patterns (e.g. {job=\"foo\", namespace=\"bar\"})",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the query in RFC3339 format or relative time (e.g. 'now-1h') (defaults to 1 hour ago)",
      "type": "string"
    },
    "step": {
      "description": "Optionally, the query resolution step (e.g. '5m')",
      "type": "string"
    }
  },
  "required": [
    "org",
    "logql"
  ],
  "type": "object"
}
```

## `query_loki_stats`

```text
Get index statistics for a Loki stream selector: streams, chunks, index entries, bytes; not log line counts.

Retrieves index-level statistics about log streams matching a given selector within a Loki or VictoriaLogs datasource and time range. Returns an object containing the count of streams, chunks, entries, and total bytes (e.g., `{"streams": 5, "chunks": 50, "entries": 10000, "bytes": 512000}`). **Important**: the `entries` count reflects storage-level index entries (chunk metadata), NOT the number of individual log lines matching the selector. Counting actual matching log lines requires a `count_over_time()` LogQL metric query. On VictoriaLogs only `entries` is populated; the other fields remain zero. The `logql` parameter **must** be a simple label selector (e.g., `{app="nginx", env="prod"}`) and does not support line filters, parsers, or aggregations. Defaults to the last hour if the time range is omitted.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endRfc3339": {
      "description": "Optionally, the end time of the query in RFC3339 format or relative time (e.g. 'now')",
      "type": "string"
    },
    "logql": {
      "description": "The LogQL matcher expression to execute. This parameter only accepts label matcher expressions and does not support full LogQL queries. Line filters, pattern operations, and metric aggregations are not supported by the stats API endpoint. Only simple label selectors can be used here.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "startRfc3339": {
      "description": "Optionally, the start time of the query in RFC3339 format or relative time (e.g. 'now-1h')",
      "type": "string"
    }
  },
  "required": [
    "org",
    "logql"
  ],
  "type": "object"
}
```

## `query_prometheus`

```text
Run a PromQL query, instant or range, on the org's Mimir metrics.

Query a PromQL-compatible datasource (Prometheus, Thanos, Mimir, Cloud Monitoring, etc.) using a PromQL expression. Supports instant queries (single point) and range queries (time range). Time: RFC3339 or relative expressions like 'now', 'now-1h'.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endTime": {
      "description": "The end time. Supported formats are RFC3339 or relative to now (e.g. 'now', 'now-1.5h', 'now-2h45m'). Valid time units are 'ns', 'us' (or 'µs'), 'ms', 's', 'm', 'h', 'd'.",
      "type": "string"
    },
    "expr": {
      "description": "The PromQL expression to query",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "projectName": {
      "description": "GCP project name to query (Cloud Monitoring datasources only). Overrides or substitutes the defaultProject configured on the datasource.",
      "type": "string"
    },
    "queryType": {
      "description": "The type of query to use. Either 'range' or 'instant'",
      "type": "string"
    },
    "startTime": {
      "description": "The start time. Required if queryType is 'range', ignored if queryType is 'instant' Supported formats are RFC3339 or relative to now (e.g. 'now', 'now-1.5h', 'now-2h45m'). Valid time units are 'ns', 'us' (or 'µs'), 'ms', 's', 'm', 'h', 'd'.",
      "type": "string"
    },
    "stepSeconds": {
      "description": "The time series step size in seconds. Required if queryType is 'range', ignored if queryType is 'instant'",
      "type": "integer"
    }
  },
  "required": [
    "org",
    "expr",
    "endTime"
  ],
  "type": "object"
}
```

## `query_prometheus_histogram`

```text
Query Prometheus histogram percentiles from classic histogram '_bucket' series.

Generates histogram_quantile PromQL. Example: metric='http_duration', percentile=95, labels='job="api"'

Time formats: 'now-1h', '2026-02-02T19:00:00Z', '1738519200000' (Unix ms)
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "endTime": {
      "description": "End time (default: now). Supports RFC3339, relative, or Unix ms.",
      "type": "string"
    },
    "labels": {
      "description": "Label selector (e.g. job=\"api\", service=\"gateway\")",
      "type": "string"
    },
    "metric": {
      "description": "Base histogram metric name (without _bucket suffix)",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "percentile": {
      "description": "Percentile to calculate (e.g. 50, 90, 95, 99)",
      "type": "number"
    },
    "projectName": {
      "description": "GCP project name to query (Cloud Monitoring datasources only). Overrides or substitutes the defaultProject configured on the datasource.",
      "type": "string"
    },
    "rateInterval": {
      "description": "Rate interval for the query (default: 5m)",
      "type": "string"
    },
    "startTime": {
      "description": "Start time (default: now-1h). Supports RFC3339, relative (now-1h), or Unix ms.",
      "type": "string"
    },
    "stepSeconds": {
      "description": "Step size in seconds for range query (default: 60)",
      "type": "integer"
    }
  },
  "required": [
    "org",
    "metric",
    "percentile"
  ],
  "type": "object"
}
```

## `query_tempo_metrics`

```text
Compute metrics from traces with a TraceQL metrics query, instant or range (default).

Compute trace-derived metrics using a TraceQL metrics query. The syntax is unlike PromQL. Use type 'instant' for a single value or 'range' for a time series (default). Instant queries over large time ranges may timeout — keep the window under 15 minutes for instant, or use range instead.
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "end": {
      "description": "End time (RFC3339 format). If not provided will search the past 1 hour.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "query": {
      "description": "TraceQL metrics query of the form '{ <selector> } | <aggregation> [by (<attribute>)]'. Example: '{ } | quantile_over_time(span:duration, .99) by (span.name)'.",
      "type": "string"
    },
    "start": {
      "description": "Start time (RFC3339 format). If not provided will search the past 1 hour.",
      "type": "string"
    },
    "type": {
      "default": "range",
      "description": "Query type: 'instant' returns a single value at the end of the time range; 'range' returns a time series. Default is 'range'.",
      "enum": [
        "instant",
        "range"
      ],
      "type": "string"
    }
  },
  "required": [
    "org",
    "query"
  ],
  "type": "object"
}
```

## `run_panel_query`

```text
Run one query (default the first) of each given Grafana dashboard panel, with variables and macros substituted.

Executes one or more dashboard panel queries with optional time range and variable overrides. Accepts an array of panel IDs to query in a single call. Fetches the dashboard, extracts queries from the specified panels, substitutes template variables and Grafana macros ($__range, $__rate_interval, $__interval), and routes to the appropriate datasource (Prometheus, Loki, ClickHouse, CloudWatch, InfluxDB, BigQuery, MSSQL, or PostgreSQL). Returns results keyed by panel ID - partial failures are allowed (some panels can succeed while others fail). If a panel uses a template variable datasource you cannot access, provide datasourceUid and datasourceType to override.
```

```json
{
  "properties": {
    "dashboardUid": {
      "description": "Dashboard UID",
      "type": "string"
    },
    "datasourceType": {
      "description": "Fallback datasource type used only when the datasource cannot be read (prometheus, loki, grafana-clickhouse-datasource, cloudwatch, influxdb, grafana-bigquery-datasource, mssql, grafana-postgresql-datasource, postgres). When the datasource is readable its real type is used and this is ignored.",
      "type": "string"
    },
    "datasourceUid": {
      "description": "Override datasource UID",
      "type": "string"
    },
    "end": {
      "description": "Override end time (e.g. 'now', RFC3339, Unix ms)",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "panelIds": {
      "description": "Panel IDs to execute (one or more)",
      "items": {
        "type": "integer"
      },
      "type": "array"
    },
    "queryIndex": {
      "description": "Index of the query to execute per panel (0-based, defaults to 0).",
      "type": "integer"
    },
    "start": {
      "description": "Override start time (e.g. 'now-1h', RFC3339, Unix ms)",
      "type": "string"
    },
    "variables": {
      "additionalProperties": {
        "type": "string"
      },
      "description": "Override dashboard variables (e.g. {\"job\": \"api-server\"})",
      "type": "object"
    }
  },
  "required": [
    "org",
    "dashboardUid",
    "panelIds"
  ],
  "type": "object"
}
```

## `search_dashboards`

```text
Search Grafana dashboards by query, folder, tag or starred: title, UID, folder, tags and URL.

Search Grafana dashboards by query and/or folderUid, tag, and starred. Returns matching dashboards with title, UID, folder, tags, and URL.
```

```json
{
  "properties": {
    "folderUid": {
      "description": "Optional immediate folder UID (not nested)",
      "type": "string"
    },
    "limit": {
      "default": 50,
      "description": "Maximum number of results to return (max 100)",
      "type": "integer"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "page": {
      "default": 1,
      "description": "Page number for pagination (1-indexed)",
      "type": "integer"
    },
    "query": {
      "description": "Optional query. Omit to filter by folderUid, tag, or starred",
      "type": "string"
    },
    "starred": {
      "description": "If true, only starred dashboards",
      "type": "boolean"
    },
    "tag": {
      "description": "Optional tags to match (AND)",
      "items": {
        "type": "string"
      },
      "type": "array"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `search_folders`

```text
Search for Grafana folders by a query string. Returns matching folders with details like title, UID, and URL.
```

```json
{
  "properties": {
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "query": {
      "description": "The query to search for",
      "type": "string"
    }
  },
  "required": [
    "org"
  ],
  "type": "object"
}
```

## `search_tempo_traces`

```text
Search for traces using TraceQL queries
```

```json
{
  "properties": {
    "datasourceUid": {
      "description": "Optional datasource UID; default the org's first of this type, usually gs-{kind} (all tenants). One tenant: gs-{kind}-{tenant}. See list_datasources.",
      "type": "string"
    },
    "end": {
      "description": "End time for the search (RFC3339 format). If not provided will search the past 1 hour. If provided, must be after start.",
      "type": "string"
    },
    "org": {
      "description": "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs.",
      "type": "string"
    },
    "query": {
      "description": "TraceQL query string",
      "type": "string"
    },
    "start": {
      "description": "Start time for the search (RFC3339 format). If not provided will search the past 1 hour. If provided, must be before end.",
      "type": "string"
    }
  },
  "required": [
    "org",
    "query"
  ],
  "type": "object"
}
```
