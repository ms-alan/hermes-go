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

// DiscordConfig holds Discord bot configuration.
type DiscordConfig struct {
	BotToken    string // Discord bot token
	ApplicationID string // Discord application ID
	// WebhookURL is the webhook URL for receiving messages (from the channel integrations).
	WebhookURL  string
}

// DiscordAdapter implements PlatformAdapter for Discord Bot API.
type DiscordAdapter struct {
	config DiscordConfig
	logger *slog.Logger
	httpClient *http.Client
}

// NewDiscordAdapter creates a Discord adapter.
func NewDiscordAdapter(cfg DiscordConfig, logger *slog.Logger) *DiscordAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &DiscordAdapter{
		config:    cfg,
		logger:    logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Platform returns the platform identifier.
func (a *DiscordAdapter) Platform() Platform {
	return PlatformDiscord
}

// Connect verifies the bot token with the Discord API.
func (a *DiscordAdapter) Connect(ctx context.Context) error {
	if a.config.BotToken == "" {
		return fmt.Errorf("Discord bot token is required (set DISCORD_BOT_TOKEN)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+a.config.BotToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Discord bot token invalid: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Discord API returned %d", resp.StatusCode)
	}
	a.logger.Info("Discord adapter connected")
	return nil
}

// Disconnect is a no-op for Discord (webhook is stateless).
func (a *DiscordAdapter) Disconnect(ctx context.Context) error {
	a.logger.Info("Discord adapter disconnected")
	return nil
}

// Send sends a message via Discord webhook or API.
func (a *DiscordAdapter) Send(ctx context.Context, out *OutboundMessage) (*SendResult, error) {
	return a.SendText(ctx, out.ChatID, out.Content)
}

// SendText sends a plain text message to a Discord channel via webhook.
func (a *DiscordAdapter) SendText(ctx context.Context, channelID, text string) (*SendResult, error) {
	if channelID == "" {
		return &SendResult{Success: false, Error: "channel_id required"}, nil
	}
	// If we have a webhook URL, use it; otherwise use the bot API.
	var url string
	var err error
	if a.config.WebhookURL != "" {
		url = a.config.WebhookURL
	} else {
		// Use Discord Bot API to send message
		url = fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", channelID)
		err = nil
	}

	payload := map[string]any{
		"content": text,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return &SendResult{Success: false, Error: err.Error()}, nil
	}

	var req *http.Request
	if a.config.WebhookURL != "" {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	}
	if err != nil {
		return &SendResult{Success: false, Error: err.Error()}, nil
	}

	if a.config.WebhookURL != "" {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Authorization", "Bot "+a.config.BotToken)
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return &SendResult{Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return &SendResult{Success: false, Error: fmt.Sprintf("Discord API %d: %s", resp.StatusCode, string(body))}, nil
	}
	return &SendResult{Success: true}, nil
}

// HandleWebhook processes an incoming Discord interaction (slash command or message).
// Discord sends interactions as JSON with a type field.
func (a *DiscordAdapter) HandleWebhook(payload []byte) (*InboundMessage, error) {
	var interaction struct {
		ID   string `json:"id"`
		Type int    `json:"type"`
		Token string `json:"token"`
		Member *struct {
			User struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"member"`
		User *struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
		Data *struct {
			Name string `json:"name"`
			Options []struct {
				Name string `json:"name"`
				Value any `json:"value"`
			} `json:"options"`
		} `json:"data"`
		Message *struct {
			Content string `json:"content"`
			ChannelID string `json:"channel_id"`
		} `json:"message"`
		ChannelID string `json:"channel_id"`
	}

	if err := json.Unmarshal(payload, &interaction); err != nil {
		return nil, fmt.Errorf("parse Discord interaction: %w", err)
	}

	// Interaction types: 1 = Ping, 2 = ApplicationCommand, 3 = MessageComponent
	switch interaction.Type {
	case 1:
		// Ping — respond with Pong
		return nil, nil // handled separately
	case 2, 3:
		// Application command or message component
		var userID, username, content, channelID string
		if interaction.Member != nil {
			userID = interaction.Member.User.ID
			username = interaction.Member.User.Username
		} else if interaction.User != nil {
			userID = interaction.User.ID
			username = interaction.User.Username
		}
		channelID = interaction.ChannelID
		if interaction.ChannelID == "" && interaction.Message != nil {
			channelID = interaction.Message.ChannelID
		}

		if interaction.Type == 2 && interaction.Data != nil {
			// Slash command — assemble content from command name + options
			content = "/" + interaction.Data.Name
			for _, opt := range interaction.Data.Options {
				content += fmt.Sprintf(" %v", opt.Value)
			}
		} else if interaction.Message != nil {
			content = interaction.Message.Content
		}

		return &InboundMessage{
			Platform:    PlatformDiscord,
			ChatID:      channelID,
			UserID:      userID,
			Username:    username,
			Content:     content,
			MessageID:   interaction.ID,
			Type:        MsgTypeChannel,
			Timestamp:   time.Now(),
			IsMentioned: true,
			RawEvent:    string(payload),
		}, nil
	}

	return nil, nil
}

// ReplyToInteraction sends a follow-up response to a Discord interaction.
// Use this to acknowledge slash commands.
func (a *DiscordAdapter) ReplyToInteraction(token, content string) error {
	url := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s/messages/@original", a.config.ApplicationID, token)
	payload := map[string]any{"content": content}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+a.config.BotToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Discord API returned %d", resp.StatusCode)
	}
	return nil
}
