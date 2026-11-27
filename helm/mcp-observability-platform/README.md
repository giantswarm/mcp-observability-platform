# mcp-observability-platform

CIMD is enabled by default. DCR is deprecated by MCP 2026-07-28 and remains a
compatibility fallback; see the [OAuth registration guide](../../docs/oauth-client-registration.md).

Giant Swarm observability-platform MCP server

![Version: 0.6.0](https://img.shields.io/badge/Version-0.6.0-informational?style=flat-square) ![AppVersion: 0.6.0](https://img.shields.io/badge/AppVersion-0.6.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Giant Swarm |  | <https://giantswarm.io> |

## Installing

```sh
helm install mcp-observability-platform helm/mcp-observability-platform \
  --namespace observability --create-namespace
```

## Example overlays

| Values file                | Scenario                                                         |
| -------------------------- | ---------------------------------------------------------------- |
| `values-memory.yaml`       | Dev/test: memory-backed OAuth store, debug logging.              |
| `values-valkey.yaml`       | Prod: Valkey-backed OAuth store (durable, shared across replicas). |
| `values-rbac-minimal.yaml` | Externally-managed ServiceAccount + ClusterRoleBinding.          |
| `values-autoscaling.yaml`  | HPA + VPA (Initial) + PDB + NetworkPolicy (ingress + egress).    |

## Configuration

See [`values.yaml`](values.yaml). Runtime tunables (tool timeout, response
cap, OAuth trust config) live under `runtime:` / `oauth:` and are delivered
to the container via an `envFrom`-mounted ConfigMap so they can be changed
without redeploying the chart.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| nameOverride | string | `""` |  |
| fullnameOverride | string | `""` |  |
| image.repository | string | `"gsoci.azurecr.io/giantswarm/mcp-observability-platform"` |  |
| image.tag | string | `""` |  |
| image.pullPolicy | string | `"IfNotPresent"` |  |
| imagePullSecrets | list | `[]` |  |
| replicas | int | `1` |  |
| resources.requests.cpu | string | `"50m"` |  |
| resources.requests.memory | string | `"128Mi"` |  |
| resources.limits.cpu | string | `"500m"` |  |
| resources.limits.memory | string | `"512Mi"` |  |
| podSecurityContext.runAsNonRoot | bool | `true` |  |
| podSecurityContext.runAsUser | int | `1000` |  |
| podSecurityContext.runAsGroup | int | `1000` |  |
| podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| containerSecurityContext.allowPrivilegeEscalation | bool | `false` |  |
| containerSecurityContext.readOnlyRootFilesystem | bool | `true` |  |
| containerSecurityContext.capabilities.drop[0] | string | `"ALL"` |  |
| service.type | string | `"ClusterIP"` |  |
| service.port | int | `8080` |  |
| service.metricsPort | int | `9091` |  |
| service.annotations | object | `{}` |  |
| serviceAccount.create | bool | `true` |  |
| serviceAccount.name | string | `""` |  |
| serviceAccount.annotations | object | `{}` |  |
| serviceMonitor.enabled | bool | `true` |  |
| serviceMonitor.interval | string | `"30s"` |  |
| serviceMonitor.labels."observability.giantswarm.io/tenant" | string | `"giantswarm"` |  |
| serviceMonitor.relabelings | list | `[]` |  |
| serviceMonitor.metricRelabelings | list | `[]` |  |
| podDisruptionBudget.enabled | bool | `false` |  |
| podDisruptionBudget.minAvailable | int | `1` |  |
| networkPolicy.enabled | bool | `true` |  |
| networkPolicy.policyTypes[0] | string | `"Ingress"` |  |
| networkPolicy.ingress[0].from[0].namespaceSelector.matchLabels."kubernetes.io/metadata.name" | string | `"agent-platform"` |  |
| networkPolicy.ingress[0].from[0].podSelector.matchLabels."app.kubernetes.io/name" | string | `"muster"` |  |
| networkPolicy.ingress[0].from[1].namespaceSelector.matchLabels."kubernetes.io/metadata.name" | string | `"agent-platform"` |  |
| networkPolicy.ingress[0].from[1].podSelector.matchLabels."app.kubernetes.io/name" | string | `"agentgateway"` |  |
| networkPolicy.ingress[0].from[2].namespaceSelector.matchLabels."kubernetes.io/metadata.name" | string | `"envoy-gateway-system"` |  |
| networkPolicy.ingress[0].from[2].podSelector.matchLabels."app.kubernetes.io/name" | string | `"envoy"` |  |
| networkPolicy.ingress[0].from[2].podSelector.matchLabels."gateway.envoyproxy.io/owning-gateway-name" | string | `"giantswarm-default"` |  |
| networkPolicy.ingress[0].from[2].podSelector.matchLabels."gateway.envoyproxy.io/owning-gateway-namespace" | string | `"envoy-gateway-system"` |  |
| networkPolicy.ingress[0].ports[0].port | string | `"http"` |  |
| networkPolicy.ingress[0].ports[0].protocol | string | `"TCP"` |  |
| networkPolicy.ingress[1].from[0].namespaceSelector.matchLabels."kubernetes.io/metadata.name" | string | `"kube-system"` |  |
| networkPolicy.ingress[1].from[0].podSelector.matchLabels."app.kubernetes.io/name" | string | `"alloy"` |  |
| networkPolicy.ingress[1].from[0].podSelector.matchLabels."app.kubernetes.io/instance" | string | `"alloy-metrics"` |  |
| networkPolicy.ingress[1].ports[0].port | string | `"metrics"` |  |
| networkPolicy.ingress[1].ports[0].protocol | string | `"TCP"` |  |
| networkPolicy.egress | list | `[]` |  |
| httpRoute.enabled | bool | `false` |  |
| httpRoute.annotations | object | `{}` |  |
| httpRoute.parentRefs[0].name | string | `"giantswarm-default"` |  |
| httpRoute.parentRefs[0].namespace | string | `"envoy-gateway-system"` |  |
| httpRoute.parentRefs[0].sectionName | string | `"https"` |  |
| httpRoute.hostnames | list | `[]` |  |
| httpRoute.matches[0].path.type | string | `"PathPrefix"` |  |
| httpRoute.matches[0].path.value | string | `"/"` |  |
| runtime.toolTimeout | string | `"30s"` |  |
| runtime.maxResponseBytes | int | `131072` |  |
| tools.disabled | list | `[]` |  |
| autoscaling.hpa.enabled | bool | `false` |  |
| autoscaling.hpa.minReplicas | int | `1` |  |
| autoscaling.hpa.maxReplicas | int | `5` |  |
| autoscaling.hpa.targetCPUUtilizationPercentage | int | `70` |  |
| autoscaling.hpa.targetMemoryUtilizationPercentage | int | `80` |  |
| autoscaling.hpa.behavior | object | `{}` |  |
| autoscaling.vpa.enabled | bool | `false` |  |
| autoscaling.vpa.updateMode | string | `"Auto"` |  |
| autoscaling.vpa.resourcePolicy | object | `{}` |  |
| mcp.transport | string | `"streamable-http"` |  |
| mcp.debug | bool | `false` |  |
| oauth.issuer | string | `"https://mcp-observability-platform.example.com"` |  |
| oauth.enableAuthorizationServer | bool | `true` |  |
| oauth.resource | string | `""` |  |
| oauth.authorizationServers | list | `[]` |  |
| oauth.allowInsecureHTTP | bool | `false` |  |
| oauth.allowPublicClientRegistration | bool | `true` |  |
| oauth.dex.issuerUrl | string | `"https://dex.example.com"` |  |
| oauth.dex.clientId | string | `""` |  |
| oauth.dex.existingSecret | string | `"mcp-observability-platform-oauth"` |  |
| oauth.storage.backend | string | `"memory"` |  |
| oauth.storage.valkey.addr | string | `""` |  |
| oauth.storage.valkey.tls | bool | `false` |  |
| oauth.storage.valkey.existingSecret | string | `""` |  |
| oauth.encryption.existingSecret | string | `""` |  |
| oauth.trustedAudiences | list | `[]` |  |
| oauth.trustedIssuers | list | `[]` |  |
| oauth.trustedRedirectSchemes | list | `[]` |  |
| grafana.url | string | `"https://grafana.example.com"` |  |
| grafana.publicUrl | string | `""` |  |
| grafana.existingSecret | string | `"mcp-observability-platform-grafana"` |  |
| grafana.authMode | string | `"jwt"` |  |
| grafana.jwtHeader | string | `"X-JWT-Assertion"` |  |
| otel.endpoint | string | `""` |  |
| otel.protocol | string | `"http/protobuf"` |  |
| otel.insecure | bool | `true` |  |
| otel.extraEnv | list | `[]` |  |
| rbac.create | bool | `true` |  |
| extraEnvVars | list | `[]` |  |
| extraEnvFrom | list | `[]` |  |
| podLabels | object | `{}` |  |
| podAnnotations | object | `{}` |  |
| nodeSelector | object | `{}` |  |
| tolerations | list | `[]` |  |
| affinity | object | `{}` |  |
| topologySpreadConstraints | list | `[]` |  |
| priorityClassName | string | `""` |  |
