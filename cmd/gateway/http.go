package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// httpServer is a minimal HTTP API server for the gateway.
type httpServer struct {
	mux       *http.ServeMux
	sessAgent *agent.SessionAgent
	logger    *slog.Logger
}

// newHTTPServer creates an HTTP API server.
func newHTTPServer(sessAgent *agent.SessionAgent, logger *slog.Logger) *httpServer {
	s := &httpServer{
		mux:       http.NewServeMux(),
		sessAgent: sessAgent,
		logger:    logger,
	}
	s.registerRoutes()
	return s
}

func (s *httpServer) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /v1/chat", s.handleChat)
	s.mux.HandleFunc("GET /v1/tools", s.handleTools)
}

func (s *httpServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *httpServer) handleChat(w http.ResponseWriter, r *http.Request) {
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

	resp, err := s.sessAgent.Chat(r.Context(), req.Message)
	if err != nil {
		s.logger.Error("chat error", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(chatResponse{Message: resp})
}

func (s *httpServer) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	names := tools.List()
	json.NewEncoder(w).Encode(map[string]any{"tools": names})
}

func (s *httpServer) Serve(addr string) error {
	s.logger.Info("HTTP API server listening", "addr", addr)
	return http.ListenAndServe(addr, s.mux)
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
