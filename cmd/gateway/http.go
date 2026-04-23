package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/gateway"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// httpServer embeds *http.Server to support graceful shutdown via Shutdown().
type httpServer struct {
	*http.Server
	sessAgent       *agent.SessionAgent
	telegramAdapter *gateway.TelegramAdapter
	discordAdapter  *gateway.DiscordAdapter
	logger          *slog.Logger
}

// newHTTPServer creates an HTTP API server.
func newHTTPServer(sessAgent *agent.SessionAgent, logger *slog.Logger) *httpServer {
	mux := http.NewServeMux()
	srv := &httpServer{
		Server:   &http.Server{Handler: mux},
		sessAgent: sessAgent,
		logger:   logger,
	}
	mux.HandleFunc("GET /health", healthHandler(logger))
	mux.HandleFunc("POST /v1/chat", chatHandler(sessAgent, logger))
	mux.HandleFunc("GET /v1/tools", toolsHandler(logger))
	return srv
}

// RegisterTelegramAdapter registers the Telegram webhook handler.
func (s *httpServer) RegisterTelegramAdapter(adapter *gateway.TelegramAdapter) {
	s.telegramAdapter = adapter
	mux := s.Server.Handler.(*http.ServeMux)
	mux.HandleFunc("POST /telegram/webhook", s.handleTelegramWebhook)
}

// RegisterDiscordAdapter registers the Discord webhook handler.
func (s *httpServer) RegisterDiscordAdapter(adapter *gateway.DiscordAdapter) {
	s.discordAdapter = adapter
	mux := s.Server.Handler.(*http.ServeMux)
	mux.HandleFunc("POST /discord/webhook", s.handleDiscordWebhook)
}

// handleTelegramWebhook processes incoming Telegram webhook updates.
func (s *httpServer) handleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg, err := s.telegramAdapter.HandleWebhook(payload)
	if err != nil {
		s.logger.Error("Telegram webhook error", "error", err)
		http.Error(w, "processing error", http.StatusInternalServerError)
		return
	}
	if msg == nil {
		// Ping interaction — respond 200 OK
		w.WriteHeader(http.StatusOK)
		return
	}

	response, err := s.sessAgent.Chat(r.Context(), msg.Content)
	if err != nil {
		s.logger.Error("chat error", "error", err)
		response = "Sorry, an error occurred."
	}
	_, _ = s.telegramAdapter.SendText(r.Context(), msg.ChatID, response)
	w.WriteHeader(http.StatusOK)
}

// handleDiscordWebhook processes incoming Discord interaction webhooks.
func (s *httpServer) handleDiscordWebhook(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Parse interaction type to handle Ping (type=1) immediately.
	var interactBase struct {
		Type int `json:"type"`
	}
	if err := json.Unmarshal(payload, &interactBase); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Respond to Ping immediately (Discord requires <3s response).
	if interactBase.Type == 1 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":1}`))
		return
	}

	msg, err := s.discordAdapter.HandleWebhook(payload)
	if err != nil {
		s.logger.Error("Discord webhook error", "error", err)
		http.Error(w, "processing error", http.StatusInternalServerError)
		return
	}
	if msg == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	response, err := s.sessAgent.Chat(r.Context(), msg.Content)
	if err != nil {
		s.logger.Error("chat error", "error", err)
		response = "Sorry, an error occurred."
	}
	_, _ = s.discordAdapter.SendText(r.Context(), msg.ChatID, response)
	w.WriteHeader(http.StatusOK)
}

// healthHandler handles GET /health.
func healthHandler(logger *slog.Logger) http.HandlerFunc {
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
