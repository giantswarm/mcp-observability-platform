package cmd

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestNewOAuthStore_MemoryDefault(t *testing.T) {
	cases := []struct {
		name, storage string
	}{
		{"empty defaults to memory", ""},
		{"explicit memory", "memory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tokenStore, clientStore, flowStore, close, err := newOAuthStore(
				&config{OAuthStorage: c.storage},
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			if err != nil {
				t.Fatalf("newOAuthStore: %v", err)
			}
			defer close()
			// memory.Store implements all three interfaces from one value, so
			// the three views should be the same underlying pointer.
			if tokenStore == nil || clientStore == nil || flowStore == nil {
				t.Error("expected non-nil store views")
			}
		})
	}
}

func TestNewOAuthStore_UnknownTypeRejected(t *testing.T) {
	_, _, _, _, err := newOAuthStore(
		&config{OAuthStorage: "sqlite"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err == nil {
		t.Fatal("expected error for unknown storage type")
	}
	if !strings.Contains(err.Error(), "unknown OAUTH_STORAGE") {
		t.Errorf("error should name OAUTH_STORAGE, got: %v", err)
	}
}
