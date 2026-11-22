package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func TestInstrumentPreservesLegacyCounters(t *testing.T) {
	for _, test := range []struct {
		name     string
		result   *mcp.CallToolResult
		err      error
		failures float64
	}{
		{name: "success", result: &mcp.CallToolResult{}},
		{name: "tool_error", result: &mcp.CallToolResult{IsError: true}, failures: 1},
		{name: "protocol_error", err: errors.New("private downstream failure"), failures: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := "counter_" + test.name
			calls := observability.ToolCallTotal.WithLabelValues(name)
			failures := observability.ToolCallErrorsTotal.WithLabelValues(name)
			beforeCalls, beforeFailures := testutil.ToFloat64(calls), testutil.ToFloat64(failures)
			result, err := Instrument()(func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return test.result, test.err })(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name}})
			require.Same(t, test.result, result)
			require.Equal(t, test.err, err)
			require.Equal(t, beforeCalls+1, testutil.ToFloat64(calls))
			require.Equal(t, beforeFailures+test.failures, testutil.ToFloat64(failures))
		})
	}
}
