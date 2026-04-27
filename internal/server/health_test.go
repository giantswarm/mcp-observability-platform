package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthChecker_Liveness_AlwaysOK(t *testing.T) {
	h := NewHealthChecker("test", 2*time.Second)
	// Register a probe that would fail readiness; liveness must still pass.
	h.Register("broken", func(ctx context.Context) (any, error) { return nil, errors.New("boom") })
	rec := httptest.NewRecorder()
	h.Liveness(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness code = %d, want 200", rec.Code)
	}
}

func TestHealthChecker_Readiness_503OnFailure(t *testing.T) {
	h := NewHealthChecker("test", 2*time.Second)
	h.Register("fine", func(ctx context.Context) (any, error) { return nil, nil })
	h.Register("broken", func(ctx context.Context) (any, error) { return nil, errors.New("boom") })

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness with one failure = %d, want 503", rec.Code)
	}
}

func TestHealthChecker_Readiness_200WhenAllPass(t *testing.T) {
	h := NewHealthChecker("test", 2*time.Second)
	h.Register("a", func(ctx context.Context) (any, error) { return nil, nil })
	h.Register("b", func(ctx context.Context) (any, error) { return nil, nil })

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness all-pass = %d, want 200", rec.Code)
	}
}

func TestHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/created":
			w.WriteHeader(http.StatusCreated)
		case "/redirect":
			w.Header().Set("Location", "/ok")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	// Disable redirect following so /redirect surfaces as a 301 response
	// the probe can inspect, instead of the client transparently
	// resolving it to /ok and returning 200.
	client := *srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	cases := []struct {
		path    string
		wantErr bool
	}{
		{"/ok", false},
		{"/created", false}, // 2xx (not just 200) must pass
		{"/redirect", true}, // 3xx must fail
		{"/fail", true},     // 5xx must fail
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			probe := HTTPProbe(&client, srv.URL+c.path)
			_, err := probe(context.Background())
			if c.wantErr && err == nil {
				t.Fatalf("%s: want error, got nil", c.path)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("%s: unexpected error: %v", c.path, err)
			}
		})
	}
}
