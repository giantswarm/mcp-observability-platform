package cmd

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateTransport(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"streamable-http", true},
		{"stdio", true},
		{"sse", true},
		{"", false},
		{"HTTP", false},            // case-sensitive; only lowercase is accepted
		{"streamable_http", false}, // underscore, not hyphen
	}
	for _, c := range cases {
		err := validateTransport(c.in)
		if c.wantOK && err != nil {
			t.Errorf("validateTransport(%q) = %v, want nil", c.in, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("validateTransport(%q) = nil, want error", c.in)
		}
		if !c.wantOK && err != nil && !strings.Contains(err.Error(), "is not supported") {
			t.Errorf("validateTransport(%q) error = %v, want 'is not supported'", c.in, err)
		}
	}
}

// TestShutdown_DrainsInFlightRequests pins down the contract runServe
// relies on for the two-phase MCP+observability drain at cmd/serve.go:
// http.Server.Shutdown must let in-flight handlers complete up to the
// drain context's deadline while refusing new connections. If Go's
// stdlib behaviour ever drifts, or a future change shortens the 10s drain
// budget in a way that starves real tool calls, this test fails before
// production does.
func TestShutdown_DrainsInFlightRequests(t *testing.T) {
	handlerStart := make(chan struct{})
	handlerRelease := make(chan struct{})
	var handlerFinished atomic.Bool

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(handlerStart)
			<-handlerRelease
			handlerFinished.Store(true)
			_, _ = w.Write([]byte("ok"))
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()

	// Fire an in-flight request.
	var wg sync.WaitGroup
	wg.Add(1)
	var (
		body   []byte
		reqErr error
	)
	go func() {
		defer wg.Done()
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			reqErr = err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, reqErr = io.ReadAll(resp.Body)
	}()

	<-handlerStart
	if handlerFinished.Load() {
		t.Fatal("handler finished before Shutdown trigger — test broken")
	}

	// Trigger the drain with a generous deadline while the handler is
	// still in flight.
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	drainDone := make(chan error, 1)
	go func() { drainDone <- srv.Shutdown(drainCtx) }()

	close(handlerRelease)

	select {
	case err := <-drainDone:
		if err != nil {
			t.Errorf("Shutdown returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return after handler released — drain stuck")
	}

	wg.Wait()
	if reqErr != nil {
		t.Fatalf("in-flight request failed: %v", reqErr)
	}
	if string(body) != "ok" {
		t.Errorf("in-flight request body = %q, want ok", body)
	}
	if !handlerFinished.Load() {
		t.Error("handler never completed")
	}
}

// TestShutdown_DeadlineStarvesSlowHandler is the companion failure-mode test.
// When a tool call runs longer than the drain budget (pathological upstream,
// missing ToolTimeout override), Shutdown returns context.DeadlineExceeded
// and the in-flight handler is cut off — which surfaces in production as a
// non-zero exit from mcpServer.Shutdown and the "mcp server drain returned
// error" warning. Documents that contract so a future naive increase in
// the drain budget (to "just let everything finish") isn't silently safer
// than running ToolTimeout-capped handlers.
func TestShutdown_DeadlineStarvesSlowHandler(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	// Fire-and-forget a slow request so Shutdown has in-flight work to stall on.
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond) // let the connection reach the server

	drainCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = srv.Shutdown(drainCtx)
	if err == nil {
		t.Fatal("Shutdown returned nil with a slow handler and tiny deadline; expected deadline-exceeded")
	}
}
