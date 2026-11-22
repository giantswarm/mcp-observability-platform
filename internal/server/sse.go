package server

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/giantswarm/mcp-toolkit/httpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
)

type sseScope struct {
	handler *mcp.SSEHandler
	streams int
}

// scopedSSE gives each validated caller profile its own native session registry.
// A stream's authenticated context is valid only for that caller profile.
type scopedSSE struct {
	server *mcp.Server
	mu     sync.Mutex
	scopes map[[32]byte]*sseScope
}

// SSEHandler serves native legacy sessions isolated by validated caller profile.
// Authentication is enforced by the caller's OAuth middleware.
func SSEHandler(server *mcp.Server) http.Handler {
	return httpx.ProtectOrigin(&scopedSSE{server: server, scopes: make(map[[32]byte]*sseScope)})
}

func (s *scopedSSE) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	caller := middleware.ExtractCaller(request)
	if !caller.Authenticated() || caller.Issuer == "" {
		http.Error(writer, "authentication required", http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	encoded, _ := json.Marshal(caller)
	key := sha256.Sum256(encoded)
	s.mu.Lock()
	scope := s.scopes[key]
	if request.Method == http.MethodGet {
		if scope == nil {
			scope = &sseScope{handler: mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return s.server }, nil)}
			s.scopes[key] = scope
		}
		scope.streams++
	}
	s.mu.Unlock()
	if scope == nil {
		http.Error(writer, "session not found", http.StatusNotFound)
		return
	}
	if request.Method == http.MethodGet {
		defer func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			scope.streams--
			if scope.streams == 0 && s.scopes[key] == scope {
				delete(s.scopes, key)
			}
		}()
	}
	ctx := middleware.InjectCallerFromRequest(request.Context(), request)
	scope.handler.ServeHTTP(writer, request.WithContext(ctx))
}
