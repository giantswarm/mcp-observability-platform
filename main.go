package main

import (
	"github.com/giantswarm/mcp-observability-platform/cmd"
)

// Version is set at build time via -ldflags.
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
