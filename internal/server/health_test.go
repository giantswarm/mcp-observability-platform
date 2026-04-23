package server

import (
	"context"
	"encoding/json"
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

func TestHealthChecker_Detailed_ShapeAndStatus(t *testing.T) {
	h := NewHealthChecker("v1.2.3", 2*time.Second)
	h.Register("ok_with_extra", func(ctx context.Context) (any, error) {
		return map[string]int{"count": 5}, nil
	})
	h.Register("failing", func(ctx context.Context) (any, error) {
		return nil, errors.New("downstream broken")
	})

	rec := httptest.NewRecorder()
	h.Detailed(rec, httptest.NewRequest(http.MethodGet, "/healthz/detailed", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("detailed with one failure = %d, want 503", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}

	var body struct {
		Status        string           `json:"status"`
		UptimeSeconds float64          `json:"uptime_seconds"`
		Version       string           `json:"version"`
		Checks        map[string]Check `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != statusFailed || body.Version != "v1.2.3" {
		t.Fatalf("body status/version = %+v", body)
	}
	if body.UptimeSeconds <= 0 {
		t.Fatalf("uptime = %v, want positive", body.UptimeSeconds)
	}
	if len(body.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(body.Checks))
	}
	if body.Checks["ok_with_extra"].Status != "ok" || body.Checks["ok_with_extra"].Extra == nil {
		t.Fatalf("ok_with_extra = %+v", body.Checks["ok_with_extra"])
	}
	if body.Checks["failing"].Status != "ok" && body.Checks["failing"].Message == "" {
		t.Fatalf("failing probe should carry a message, got %+v", body.Checks["failing"])
	}
}

func TestHealthChecker_Detailed_MarshalErrorReturns500(t *testing.T) {
	h := NewHealthChecker("test", 2*time.Second)
	// A chan cannot be marshalled to JSON, so json.Marshal on the body
	// fails. The hardening must turn this into a 500, not a truncated 200.
	h.Register("bad_extra", func(ctx context.Context) (any, error) {
		return make(chan int), nil
	})
	rec := httptest.NewRecorder()
	h.Detailed(rec, httptest.NewRequest(http.MethodGet, "/healthz/detailed", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("detailed with unmarshallable Extra = %d, want 500", rec.Code)
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
