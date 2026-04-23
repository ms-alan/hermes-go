package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nousresearch/hermes-go/pkg/gateway"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// Adapter implements the QQ Bot platform adapter.
type Adapter struct {
	config    *Config
	api       *APIClient
	wsURL     string
	conn      *websocket.Conn
	logger    *slog.Logger
	running   atomic.Bool
	mu        sync.RWMutex
	seq       int64
	sessionID string
	Handler   gateway.MessageHandler
	connCtx    context.Context
	connCancel context.CancelFunc
}

// NewAdapter creates a new QQ adapter.
func NewAdapter(cfg *Config, logger *slog.Logger) (*Adapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("qqbot: config is nil")
	}
	api, err := NewAPIClient(cfg.AppID, cfg.AppSecret)
	if err != nil {
		return nil, fmt.Errorf("qqbot: create API client: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{config: cfg, api: api, logger: logger}, nil
}

func (a *Adapter) Platform() gateway.Platform { return gateway.PlatformQQ }

func (a *Adapter) Connect(ctx context.Context) error {
	if a.running.Load() { return nil }
	wsURL, err := a.api.GetGatewayURL(ctx)
	if err != nil { return fmt.Errorf("qqbot: get gateway URL: %w", err) }
	a.wsURL = wsURL
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil { return fmt.Errorf("qqbot: dial: %w", err) }
	a.conn = conn
	a.connCtx, a.connCancel = context.WithCancel(context.Background())
	if err := a.handshake(a.connCtx); err != nil {
		a.conn.Close(websocket.StatusNormalClosure, "bye")
		return err
	}
	a.running.Store(true)
	go a.readLoop(a.connCtx)
	return nil
}

func (a *Adapter) Disconnect(ctx context.Context) error {
	if !a.running.Load() { return nil }
	a.running.Store(false)
	if a.connCancel != nil { a.connCancel() }
	if a.conn != nil { a.conn.Close(websocket.StatusNormalClosure, "bye") }
	return nil
}

func (a *Adapter) IsConnected() bool { return a.running.Load() }

func (a *Adapter) Send(ctx context.Context, out *gateway.OutboundMessage) (*gateway.SendResult, error) {
	if out.ImagePath != "" {
		url, err := a.api.UploadMedia(ctx, out.ChatID, out.ImagePath, "image")
		if err != nil { return &gateway.SendResult{Success: false, Error: err.Error()}, nil }
		resp, err := a.api.SendMessage(ctx, out.ChatID, url)
		if err != nil { return &gateway.SendResult{Success: false, Error: err.Error()}, nil }
		return &gateway.SendResult{MessageID: resp.ID, Success: true}, nil
	}
	if a.config.MarkdownEnabled {
		if err := a.api.SendMarkdown(ctx, out.ChatID, out.Content); err != nil {
			return &gateway.SendResult{Success: false, Error: err.Error()}, nil
		}
		return &gateway.SendResult{Success: true}, nil
	}
	resp, err := a.api.SendMessage(ctx, out.ChatID, out.Content)
	if err != nil { return &gateway.SendResult{Success: false, Error: err.Error()}, nil }
	return &gateway.SendResult{MessageID: resp.ID, Success: true}, nil
}

func (a *Adapter) SendText(ctx context.Context, chatID, text string) (*gateway.SendResult, error) {
	return a.Send(ctx, &gateway.OutboundMessage{ChatID: chatID, Content: text})
}

func (a *Adapter) handshake(ctx context.Context) error {
	var hello WSPayload
	if err := wsjson.Read(ctx, a.conn, &hello); err != nil { return fmt.Errorf("read hello: %w", err) }
	if hello.Op != OpHello { return fmt.Errorf("expected HELLO (Op=7), got Op=%d", hello.Op) }
	var helloData HelloData
	if err := json.Unmarshal(hello.Data, &helloData); err != nil { return fmt.Errorf("parse hello: %w", err) }
	a.sessionID = helloData.SessionID
	a.logger.Info("qqbot connected", "session_id", a.sessionID)
	identify := map[string]any{
		"op": OpIdentify,
		"d": map[string]any{
			"app_id":     a.config.AppID,
			"token":      a.api.Token,
			"intents":    IntentGuildMessages | IntentDirectMessages,
			"session_id": a.sessionID,
			"properties": map[string]string{
				"os":      "hermes-go",
				"browser": "hermes-go",
				"sdk":     "1.0.0",
			},
		},
	}
	if err := wsjson.Write(ctx, a.conn, identify); err != nil { return fmt.Errorf("send identify: %w", err) }
	return nil
}

func (a *Adapter) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done(): return
		default:
		}
		var payload WSPayload
		if err := wsjson.Read(ctx, a.conn, &payload); err != nil {
			if ctx.Err() != nil { return }
			a.logger.Error("qqbot read error", "error", err)
			a.reconnect(ctx)
			return
		}
		a.mu.Lock()
		if payload.Seq > a.seq { a.seq = payload.Seq }
		a.mu.Unlock()
		switch payload.Op {
		case OpDispatch:   a.handleDispatch(ctx, payload.Data)
		case OpHeartbeatACK:
		case OpReconnect:
			a.logger.Warn("qqbot reconnect requested")
			a.reconnect(ctx)
			return
		}
	}
}

func (a *Adapter) handleDispatch(ctx context.Context, data json.RawMessage) {
	var header struct{ EventType string `json:"t"` }
	if json.Unmarshal(data, &header) != nil { return }
	switch header.EventType {
	case EventTypeReady:                a.handleReady(ctx, data)
	case EventTypeMessageCreate:       a.handleMessageCreate(ctx, data)
	case EventTypeDirectMessageCreate: a.handleDirectMessageCreate(ctx, data)
	case EventTypeAtMessageCreate:     a.handleAtMessageCreate(ctx, data)
	}
}

func (a *Adapter) handleReady(ctx context.Context, data json.RawMessage) {
	var event struct{ Data ReadyEvent `json:"d"` }
	if json.Unmarshal(data, &event) != nil { return }
	a.logger.Info("qqbot ready", "user", event.Data.User.Username)
}

func (a *Adapter) handleMessageCreate(ctx context.Context, data json.RawMessage) {
	var event struct{ Data MessageCreateEvent `json:"d"` }
	if json.Unmarshal(data, &event) != nil { return }
	if event.Data.Author == nil { return }
	msg := &gateway.InboundMessage{
		Platform:    gateway.PlatformQQ,
		ChatID:     event.Data.ChannelID,
		UserID:     event.Data.Author.ID,
		Username:   event.Data.Author.Username,
		Content:    event.Data.Content,
		MessageID:  event.Data.ID,
		Type:       gateway.MsgTypeGroup,
		Timestamp:  time.Unix(event.Data.Timestamp, 0),
		IsMentioned: true,
	}
	if a.Handler != nil { a.Handler.HandleInbound(ctx, msg) }
}

func (a *Adapter) handleDirectMessageCreate(ctx context.Context, data json.RawMessage) {
	var event struct{ Data DirectMessageCreateEvent `json:"d"` }
	if json.Unmarshal(data, &event) != nil { return }
	if event.Data.Author == nil { return }
	msg := &gateway.InboundMessage{
		Platform:   gateway.PlatformQQ,
		ChatID:    event.Data.ChannelID,
		UserID:    event.Data.Author.ID,
		Username:  event.Data.Author.Username,
		Content:   event.Data.Content,
		MessageID: event.Data.ID,
		Type:      gateway.MsgTypeDirect,
		Timestamp: time.Unix(event.Data.Timestamp, 0),
	}
	if a.Handler != nil { a.Handler.HandleInbound(ctx, msg) }
}

func (a *Adapter) handleAtMessageCreate(ctx context.Context, data json.RawMessage) { a.handleMessageCreate(ctx, data) }

func (a *Adapter) reconnect(ctx context.Context) {
	a.logger.Warn("qqbot reconnecting...")
	if a.conn != nil { a.conn.Close(websocket.StatusNormalClosure, "reconnect") }
	a.running.Store(false) // Clear so Connect() will actually reconnect
	a.connCtx, a.connCancel = context.WithCancel(context.Background())
	if err := a.Connect(ctx); err != nil {
		a.logger.Error("qqbot reconnect failed", "error", err)
	}
}
