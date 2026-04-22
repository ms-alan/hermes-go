package qqbot

import "encoding/json"

// WebSocket opcodes.
const (
	OpDispatch       = 0
	OpHeartbeat      = 1
	OpIdentify       = 2
	OpResume         = 6
	OpHello          = 7
	OpHeartbeatACK   = 11
	OpReconnect      = 12
	OpInvalidSession = 14
)

// Intent flags.
const (
	IntentGuildMessages     = 1 << 0
	IntentDirectMessages    = 1 << 1
	IntentGuildMemberEvents = 1 << 2
	IntentInteractionCreate = 1 << 3
)

// Event types.
const (
	EventTypeMessageCreate         = "MESSAGE_CREATE"
	EventTypeDirectMessageCreate   = "DIRECT_MESSAGE_CREATE"
	EventTypeAtMessageCreate       = "AT_MESSAGE_CREATE"
	EventTypePublicMessageDelete   = "PUBLIC_MESSAGE_DELETE"
	EventTypeReady                 = "READY"
)

// WSPayload is the top-level WebSocket frame.
type WSPayload struct {
	Op   int             `json:"op"`
	Seq  int64           `json:"s,omitempty"`
	Data json.RawMessage `json:"d,omitempty"`
}

// HelloData is in Op=7 HELLO.
type HelloData struct {
	URL           string `json:"url"`
	Version       string `json:"version"`
	SessionID     string `json:"session_id"`
	RetryInterval int    `json:"retry_interval"`
}

// Identify is sent by client in Op=2.
type Identify struct {
	AppID      string            `json:"app_id"`
	Token      string            `json:"token"`
	Intents    int               `json:"intents"`
	SessionID  string            `json:"session_id,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
}

// ReadyEvent is received after IDENTIFY.
type ReadyEvent struct {
	Version   string `json:"version"`
	SessionID string `json:"session_id"`
	User      *User  `json:"user"`
}

// User represents a QQ user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
	Bot      bool   `json:"bot,omitempty"`
}

// MessageCreateEvent is the payload for MESSAGE_CREATE.
type MessageCreateEvent struct {
	ID           string       `json:"id"`
	GuildID      string       `json:"guild_id"`
	ChannelID    string       `json:"channel_id"`
	Content      string       `json:"content"`
	Author       *User        `json:"author"`
	Timestamp    int64        `json:"timestamp"`
	Mentions     []*User      `json:"mentions,omitempty"`
	Attachments  []*Attachment `json:"attachments,omitempty"`
	Reactions    []*Reaction   `json:"reactions,omitempty"`
}

// DirectMessageCreateEvent is the payload for DIRECT_MESSAGE_CREATE.
type DirectMessageCreateEvent struct {
	ID        string `json:"id"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
	Author    *User  `json:"author"`
	Timestamp int64  `json:"timestamp"`
}

// AtMessageCreateEvent is for @机器人 messages.
type AtMessageCreateEvent struct {
	ID        string   `json:"id"`
	GuildID   string   `json:"guild_id"`
	ChannelID string   `json:"channel_id"`
	Content   string   `json:"content"`
	Author    *User    `json:"author"`
	Timestamp int64    `json:"timestamp"`
	Mentions  []*User  `json:"mentions,omitempty"`
}

// Attachment represents a message attachment.
type Attachment struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	MimeType string `json:"content_type"`
}

// Reaction represents a reaction.
type Reaction struct {
	Count int    `json:"count"`
	Type  int    `json:"type"`
	Emoji *Emoji `json:"emoji"`
}

// Emoji represents an emoji.
type Emoji struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// APIResponse wraps QQ API responses.
type APIResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data,omitempty"`
}

// SendMessageResponse is the response from sending a message.
type SendMessageResponse struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Timestamp string `json:"timestamp"`
}

// UploadMediaResponse is the response from uploading media.
type UploadMediaResponse struct {
	URL string `json:"url"`
}
