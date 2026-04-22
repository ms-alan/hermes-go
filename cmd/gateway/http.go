package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// httpServer embeds *http.Server to support graceful shutdown via Shutdown().
type httpServer struct {
	*http.Server
	sessAgent *agent.SessionAgent
}

// newHTTPServer creates an HTTP API server.
func newHTTPServer(sessAgent *agent.SessionAgent, logger *slog.Logger) *httpServer {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(sessAgent, logger))
	mux.HandleFunc("POST /v1/chat", chatHandler(sessAgent, logger))
	mux.HandleFunc("GET /v1/tools", toolsHandler(logger))

	srv := &http.Server{
		Handler: mux,
	}

	return &httpServer{
		Server:   srv,
		sessAgent: sessAgent,
	}
}

// healthHandler handles GET /health.
func healthHandler(sessAgent *agent.SessionAgent, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// chatHandler handles POST /v1/chat.
func chatHandler(sessAgent *agent.SessionAgent, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Message == "" {
			http.Error(w, "field 'message' is required", http.StatusBadRequest)
			return
		}

		resp, err := sessAgent.Chat(r.Context(), req.Message)
		if err != nil {
			logger.Error("chat error", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(chatResponse{Message: resp})
	}
}

// toolsHandler handles GET /v1/tools.
func toolsHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		names := tools.List()
		json.NewEncoder(w).Encode(map[string]any{"tools": names})
	}
}

// chatRequest is the POST /v1/chat request body.
type chatRequest struct {
	Message string `json:"message"`
}

// chatResponse is the POST /v1/chat response body.
type chatResponse struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}
