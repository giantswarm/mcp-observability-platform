package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

// SetVersion is called from main to inject the build-time version.
func SetVersion(v string) { version = v }

var rootCmd = &cobra.Command{
	Use:   "mcp-observability-platform",
	Short: "Giant Swarm observability-platform MCP server",
	Long: `Giant Swarm's observability-platform MCP server.

Exposes Grafana (and the underlying Mimir/Loki/Tempo/Alertmanager datasources)
to MCP clients, with per-caller tenant and role scoping derived from
GrafanaOrganization custom resources.`,
}

// Execute runs the root command. Default subcommand is "serve" when none is given.
func Execute() {
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "serve")
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)
}
