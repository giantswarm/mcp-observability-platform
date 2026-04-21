# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Go MCP server scaffold: `/mcp` streamable-HTTP transport gated by mcp-oauth (Dex IdP), Grafana admin client, `GrafanaOrganization` CR-backed authz resolver, Prometheus metrics, OTel tracing, shared tool-handler helpers. No tools registered yet — see the "Port Go tools" and "Port Helm chart" follow-up PRs for the full MCP surface and deployment artifacts.

### Changed

- `app.giantswarm.io` label group was changed to `application.giantswarm.io`

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
