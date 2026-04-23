package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// TelegramConfig holds Telegram bot configuration.
type TelegramConfig struct {
	BotToken string // Telegram Bot API token from @BotFather
}

// TelegramAdapter implements PlatformAdapter for Telegram Bot API.
type TelegramAdapter struct {
	config TelegramConfig
	logger *slog.Logger
	httpClient *http.Client
	webhookPath string // registered webhook URL path
}

// NewTelegramAdapter creates a Telegram adapter.
func NewTelegramAdapter(cfg TelegramConfig, logger *slog.Logger) *TelegramAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &TelegramAdapter{
		config: cfg,
		logger: logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Platform returns the platform identifier.
func (a *TelegramAdapter) Platform() Platform {
	return PlatformTelegram
}

// Connect registers the webhook and starts long-poll fallback.
// If a webhookPath is provided, the caller is responsible for registering
// the HTTP handler. This method just verifies the bot token.
func (a *TelegramAdapter) Connect(ctx context.Context) error {
	if a.config.BotToken == "" {
		return fmt.Errorf("Telegram bot token is required (set TELEGRAM_BOT_TOKEN)")
	}
	// Verify token by calling getMe
	resp, err := a.botRequest(ctx, "getMe", nil)
	if err != nil {
		return fmt.Errorf("Telegram bot token invalid: %w", err)
	}
	resp.Body.Close()
	a.logger.Info("Telegram adapter connected")
	return nil
}

// Disconnect is a no-op for Telegram (webhook is stateless).
func (a *TelegramAdapter) Disconnect(ctx context.Context) error {
	a.logger.Info("Telegram adapter disconnected")
	return nil
}

// Send sends a message via Telegram Bot API.
func (a *TelegramAdapter) Send(ctx context.Context, out *OutboundMessage) (*SendResult, error) {
	return a.SendText(ctx, out.ChatID, out.Content)
}

// SendText sends a plain text message to a Telegram chat.
func (a *TelegramAdapter) SendText(ctx context.Context, chatID, text string) (*SendResult, error) {
	if chatID == "" {
		return &SendResult{Success: false, Error: "chat_id required"}, nil
	}
	body := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	resp, err := a.botRequest(ctx, "sendMessage", body)
	if err != nil {
		return &SendResult{Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()

	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &SendResult{Success: false, Error: "parse response: " + err.Error()}, nil
	}
	if !result.OK {
		return &SendResult{Success: false, Error: "Telegram API error"}, nil
	}
	return &SendResult{Success: true}, nil
}

// HandleWebhook processes an incoming Telegram update (from the webhook endpoint).
// It returns the normalized InboundMessage or an error.
func (a *TelegramAdapter) HandleWebhook(payload []byte) (*InboundMessage, error) {
	var update struct {
		UpdateID int64 `json:"update_id"`
		Message  struct {
			MessageID int64  `json:"message_id"`
			Chat     struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(payload, &update); err != nil {
		return nil, fmt.Errorf("parse Telegram update: %w", err)
	}

	msgType := MsgTypeDirect
	if update.Message.Chat.Type == "group" {
		msgType = MsgTypeGroup
	}

	return &InboundMessage{
		Platform:    PlatformTelegram,
		ChatID:      fmt.Sprintf("%d", update.Message.Chat.ID),
		UserID:      fmt.Sprintf("%d", update.Message.From.ID),
		Username:    update.Message.From.Username,
		Content:     update.Message.Text,
		MessageID:   fmt.Sprintf("%d", update.Message.MessageID),
		Type:        msgType,
		Timestamp:   time.Now(),
		IsMentioned: true, // Telegram always "mentioned" in DM
	}, nil
}

// botRequest makes a request to the Telegram Bot API.
func (a *TelegramAdapter) botRequest(ctx context.Context, method string, body map[string]any) (*http.Response, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", a.config.BotToken, method)
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return a.httpClient.Do(req)
}
