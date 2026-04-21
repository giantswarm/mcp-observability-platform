package server

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func TestNew_RejectsMissingDependencies(t *testing.T) {
	// dummy non-nil values for the fields we're NOT testing in each case
	log := slog.Default()
	resolver := &authz.Resolver{} // zero value is enough for validation
	gf := &grafana.Client{}       // ditto

	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no logger", Config{Resolver: resolver, Grafana: gf}, "Logger is required"},
		{"no resolver", Config{Logger: log, Grafana: gf}, "Resolver is required"},
		{"no grafana", Config{Logger: log, Resolver: resolver}, "Grafana is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.cfg)
			if err == nil {
				t.Fatalf("New(%+v) = nil error, want %q", c.cfg, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestNew_DefaultsVersion(t *testing.T) {
	// When Version is empty, New should still succeed and default to "dev".
	// We can't easily inspect the mcp-go server's stored version, but we can
	// at least confirm construction doesn't fail on the default path.
	_, err := New(Config{
		Logger:   slog.Default(),
		Resolver: &authz.Resolver{},
		Grafana:  &grafana.Client{},
	})
	if err != nil {
		t.Fatalf("New with empty Version: %v", err)
	}
}
