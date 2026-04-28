package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealth_Liveness_AlwaysOK(t *testing.T) {
	h := NewHealth(2 * time.Second)
	// Even with a probe that would fail readiness, liveness must pass.
	h.Register("broken", func(context.Context) error { return errors.New("boom") })
	rec := httptest.NewRecorder()
	h.Liveness(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness code = %d, want 200", rec.Code)
	}
}

func TestHealth_Readiness_503OnFirstFailure(t *testing.T) {
	h := NewHealth(2 * time.Second)
	h.Register("first", func(context.Context) error { return nil })
	h.Register("broken", func(context.Context) error { return errors.New("boom") })

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness with one failure = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"broken"`) {
		t.Errorf("body should name the failing probe, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("body should include the probe error, got %q", rec.Body.String())
	}
}

func TestHealth_Readiness_200WhenAllPass(t *testing.T) {
	h := NewHealth(2 * time.Second)
	h.Register("a", func(context.Context) error { return nil })
	h.Register("b", func(context.Context) error { return nil })

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness all-pass = %d, want 200", rec.Code)
	}
}

func TestHealth_Readiness_StopsAtFirstFailure(t *testing.T) {
	h := NewHealth(2 * time.Second)
	var ranSecond bool
	h.Register("first", func(context.Context) error { return errors.New("first failed") })
	h.Register("second", func(context.Context) error { ranSecond = true; return nil })

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ranSecond {
		t.Error("second probe ran after first failed; serial-stop-at-first contract violated")
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

	// Disable redirect following so /redirect surfaces as a 301 the
	// probe can reject; otherwise the client transparently resolves it.
	client := *srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	cases := []struct {
		path    string
		wantErr bool
	}{
		{"/ok", false},
		{"/created", false}, // any 2xx must pass
		{"/redirect", true}, // 3xx must fail
		{"/fail", true},     // 5xx must fail
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			err := HTTPProbe(&client, srv.URL+c.path)(context.Background())
			if c.wantErr && err == nil {
				t.Fatalf("%s: want error, got nil", c.path)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("%s: unexpected error: %v", c.path, err)
			}
		})
	}
}
