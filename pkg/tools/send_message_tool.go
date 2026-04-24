package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/gateway"
)

// sendMessageSchema is the tool schema for send_message.
var sendMessageSchema = map[string]any{
	"name":        "send_message",
	"description": "Send a message to a connected messaging platform, or list available targets.\n\nIMPORTANT: When the user asks to send to a specific channel or person (not just a bare platform name), call send_message(action='list') FIRST to see available targets, then send to the correct one.\nIf the user just says a platform name like 'send to telegram', send directly to the home channel without listing first.\n\nSupported platforms: qq (default), telegram, discord.\nTarget formats:\n  'qq' or empty → home channel (QQ DM)\n  'qq:chat_id' → QQ channel/group by ID\n  'telegram:-1001234567890' → Telegram channel by numeric ID\n  'telegram:-1001234567890:17585' → Telegram topic/thread\n  'discord:#channel-name' → Discord channel by name\n  'discord:chat_id' → Discord channel by ID\n  'discord:chat_id:thread_id' → Discord thread",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []any{"send", "list"},
				"description": "Action: 'send' (default) sends a message. 'list' returns all available channels/contacts across connected platforms.",
			},
			"target": map[string]any{
				"type":        "string",
				"description": "Delivery target. Format: 'platform' (uses home channel), 'platform:chat_id', or 'platform:chat_id:thread_id' for Telegram topics and Discord threads. Examples: 'qq', 'qq:123456789', 'telegram:-1001234567890', 'discord:#engineering', 'discord:999888777:555444333'",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The message text to send. Supports markdown formatting on Telegram and Discord.",
			},
		},
		"required": []any{},
	},
}

// sendMessageHandler is the tool handler for send_message.
func sendMessageHandler(args map[string]any) string {
	action := getStringArg(args, "action", "send")

	if action == "list" {
		return handleListTargets()
	}
	return handleSendMessage(args)
}

// getStringArg safely extracts a string arg with a default value.
func getStringArg(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

// handleListTargets returns all available messaging targets.
func handleListTargets() string {
	adapters := gateway.ListAdapters()
	if len(adapters) == 0 {
		return toolError("No messaging platforms are currently connected. Configure QQ (QQ_APP_ID + QQ_CLIENT_SECRET), Telegram, or Discord adapters.")
	}

	type targetInfo struct {
		Platform  string `json:"platform"`
		Status    string `json:"status"`
		Note      string `json:"note,omitempty"`
	}

	targets := make([]targetInfo, 0, len(adapters))
	for _, a := range adapters {
		p := string(a.Platform())
		targets = append(targets, targetInfo{
			Platform: p,
			Status:   "connected",
			Note:     "Use target='" + p + "' for home channel, or '" + p + ":chat_id' for a specific conversation",
		})
	}

	return toolResultData(map[string]any{
		"platforms": targets,
		"note":     "All targets accept the format 'platform:chat_id' or 'platform' for home channel",
	})
}

// handleSendMessage sends a message to the specified platform target.
func handleSendMessage(args map[string]any) string {
	target := getStringArg(args, "target", "")
	message := getStringArg(args, "message", "")

	if target == "" || message == "" {
		return toolError("Both 'target' and 'message' are required when action='send'")
	}

	// Parse "platform:chat_id:thread_id" or just "platform"
	parts := strings.SplitN(target, ":", 3)
	platformStr := strings.ToLower(strings.TrimSpace(parts[0]))
	chatID := ""
	threadID := ""

	if len(parts) > 1 {
		chatID = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		threadID = strings.TrimSpace(parts[2])
	}

	// Resolve platform name to gateway.Platform
	platform := gateway.Platform(platformStr)

	// Get the adapter
	adapter := gateway.GetAdapter(platform)
	if adapter == nil {
		return toolError(fmt.Sprintf("platform %q is not connected. Use send_message(action='list') to see available platforms.", platformStr))
	}

	// Build outbound message
	out := &gateway.OutboundMessage{
		Platform: platform,
		Content:  message,
	}

	// Determine routing: use chat_id if provided, otherwise let adapter resolve
	if chatID != "" {
		out.ChatID = chatID
		out.ThreadID = threadID
	}
	// If chatID is empty, the adapter should route to the user's home/dm channel

	ctx := context.Background()
	result, err := adapter.Send(ctx, out)
	if err != nil {
		return toolError(fmt.Sprintf("send failed: %v", err))
	}

	if !result.Success {
		return toolError(fmt.Sprintf("send unsuccessful: %s", result.Error))
	}

	return toolResultData(map[string]any{
		"success":    true,
		"platform":    platformStr,
		"message_id":  result.MessageID,
		"target":      target,
	})
}
