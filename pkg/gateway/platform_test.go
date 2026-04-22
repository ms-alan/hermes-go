package gateway

import (
	"context"
	"testing"
	"time"
)

func TestPlatformConstants(t *testing.T) {
	if PlatformQQ != "qq" {
		t.Errorf("PlatformQQ = %q, want qq", PlatformQQ)
	}
	if PlatformTelegram != "telegram" {
		t.Errorf("PlatformTelegram = %q, want telegram", PlatformTelegram)
	}
	if PlatformDiscord != "discord" {
		t.Errorf("PlatformDiscord = %q, want discord", PlatformDiscord)
	}
}

func TestMessageTypeConstants(t *testing.T) {
	if MsgTypeDirect != "direct" {
		t.Errorf("MsgTypeDirect = %q, want direct", MsgTypeDirect)
	}
	if MsgTypeGroup != "group" {
		t.Errorf("MsgTypeGroup = %q, want group", MsgTypeGroup)
	}
	if MsgTypeChannel != "channel" {
		t.Errorf("MsgTypeChannel = %q, want channel", MsgTypeChannel)
	}
}

func TestInboundMessage(t *testing.T) {
	ts := time.Now()
	msg := InboundMessage{
		Platform:    PlatformQQ,
		ChatID:     "chat_123",
		UserID:     "user_456",
		Username:   "testuser",
		Content:    "hello",
		MessageID:  "msg_789",
		Type:       MsgTypeDirect,
		Timestamp:  ts,
		IsMentioned: true,
	}
	if msg.Platform != PlatformQQ {
		t.Errorf("Platform = %q, want qq", msg.Platform)
	}
	if msg.ChatID != "chat_123" {
		t.Errorf("ChatID = %q, want chat_123", msg.ChatID)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want hello", msg.Content)
	}
	if !msg.IsMentioned {
		t.Error("IsMentioned should be true")
	}
}

func TestOutboundMessage(t *testing.T) {
	msg := OutboundMessage{
		ChatID:    "chat_abc",
		Content:   "response text",
		ImagePath: "/path/to/image.png",
	}
	if msg.ChatID != "chat_abc" {
		t.Errorf("ChatID = %q, want chat_abc", msg.ChatID)
	}
	if msg.ImagePath != "/path/to/image.png" {
		t.Errorf("ImagePath = %q, want /path/to/image.png", msg.ImagePath)
	}
}

func TestSendResult(t *testing.T) {
	success := SendResult{MessageID: "msg_ok", Success: true}
	if !success.Success {
		t.Error("Success should be true")
	}
	if success.MessageID != "msg_ok" {
		t.Errorf("MessageID = %q, want msg_ok", success.MessageID)
	}

	failure := SendResult{Success: false, Error: "network error"}
	if failure.Success {
		t.Error("Success should be false")
	}
	if failure.Error != "network error" {
		t.Errorf("Error = %q, want network error", failure.Error)
	}
}

// mockAdapter implements PlatformAdapter for testing.
type mockAdapter struct {
	connectErr    error
	sendErr       error
	sendResult    *SendResult
	disconnectErr error
}

func (m *mockAdapter) Connect(ctx context.Context) error { return m.connectErr }
func (m *mockAdapter) Disconnect(ctx context.Context) error { return m.disconnectErr }
func (m *mockAdapter) Platform() Platform { return PlatformQQ }
func (m *mockAdapter) Send(ctx context.Context, out *OutboundMessage) (*SendResult, error) {
	return m.sendResult, m.sendErr
}
func (m *mockAdapter) SendText(ctx context.Context, chatID, text string) (*SendResult, error) {
	return m.sendResult, m.sendErr
}

func TestPlatformAdapterInterface(t *testing.T) {
	var a PlatformAdapter = &mockAdapter{}
	if a.Platform() != PlatformQQ {
		t.Errorf("Platform() = %q, want qq", a.Platform())
	}
}

func TestMessageHandlerInterface(t *testing.T) {
	// MessageHandler only has HandleInbound; just verify the interface is satisfied.
	var _ MessageHandler = (*mockMessageHandler)(nil)
}

type mockMessageHandler struct{}

func (*mockMessageHandler) HandleInbound(ctx context.Context, msg *InboundMessage) error {
	return nil
}
