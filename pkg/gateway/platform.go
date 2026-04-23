package gateway

import (
	"context"
	"time"
)

// Platform represents a messaging platform.
type Platform string

const (
	PlatformQQ       Platform = "qq"
	PlatformTelegram Platform = "telegram" // TODO: implement adapter
	PlatformDiscord  Platform = "discord"  // TODO: implement adapter
)

// MessageType categorizes an inbound message.
type MessageType string

const (
	MsgTypeDirect  MessageType = "direct"
	MsgTypeGroup   MessageType = "group"
	MsgTypeChannel MessageType = "channel"
)

// InboundMessage is a normalized inbound message from any platform.
type InboundMessage struct {
	Platform    Platform  `json:"platform"`
	ChatID      string   `json:"chat_id"`
	UserID      string   `json:"user_id"`
	Username    string   `json:"username"`
	Content     string   `json:"content"`
	RawEvent    any      `json:"raw_event,omitempty"`
	MessageID   string   `json:"message_id"`
	Type        MessageType `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	IsMentioned bool      `json:"is_mentioned"`
}

// OutboundMessage is what the agent wants to send to a platform.
type OutboundMessage struct {
	Platform  Platform `json:"platform"` // defaults to PlatformQQ
	ChatID    string  `json:"chat_id"`
	Content   string  `json:"content"`
	ImagePath string  `json:"image_path,omitempty"`
	ThreadID  string  `json:"thread_id,omitempty"`
}

// SendResult is the result of a send operation.
type SendResult struct {
	MessageID string `json:"message_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// PlatformAdapter is the interface all messaging platform adapters implement.
type PlatformAdapter interface {
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	Platform() Platform
	Send(ctx context.Context, out *OutboundMessage) (*SendResult, error)
	SendText(ctx context.Context, chatID, text string) (*SendResult, error)
}

// MessageHandler is the interface for receiving inbound messages.
type MessageHandler interface {
	HandleInbound(ctx context.Context, msg *InboundMessage) error
}
