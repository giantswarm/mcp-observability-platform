package middleware

import (
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		res  *mcp.CallToolResult
		err  error
		want string
	}{
		{"no result, no error", nil, nil, OutcomeOK},
		{"success result", &mcp.CallToolResult{}, nil, OutcomeOK},
		{"Go error", nil, errors.New("boom"), OutcomeSystemError},
		{"Go error wins over IsError", &mcp.CallToolResult{IsError: true}, errors.New("x"), OutcomeSystemError},
		{"IsError result only", &mcp.CallToolResult{IsError: true}, nil, OutcomeUserError},
	}
	for _, c := range cases {
		if got := Classify(c.res, c.err); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}
