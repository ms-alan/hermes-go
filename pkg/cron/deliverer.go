package cron

import (
	"context"
	"fmt"

	"github.com/nousresearch/hermes-go/pkg/gateway"
)

// QQDeliverer delivers cron job output via a gateway platform adapter.
type QQDeliverer struct {
	Adapter gateway.PlatformAdapter
}

// NewQQDeliverer creates a QQ deliverer from a platform adapter.
func NewQQDeliverer(adapter gateway.PlatformAdapter) *QQDeliverer {
	return &QQDeliverer{Adapter: adapter}
}

// Deliver sends job output to the configured destination.
func (d *QQDeliverer) Deliver(ctx context.Context, jobID string, content string, origin *DeliveryOrigin) error {
	if origin == nil {
		return fmt.Errorf("no delivery origin configured for job %s", jobID)
	}

	// Truncate very long output for QQ message
	const maxLen = 4000
	if len(content) > maxLen {
		content = content[:maxLen] + "\n\n... (truncated)"
	}

	msg := &gateway.OutboundMessage{
		Platform: gateway.Platform(origin.Platform),
		ChatID:   origin.ChatID,
		Content:  fmt.Sprintf("[Cron Job %s]\n\n%s", jobID, content),
	}
	if origin.UserID != "" {
		msg.UserID = origin.UserID
	}
	if origin.ThreadID != "" {
		msg.ThreadID = origin.ThreadID
	}

	result, err := d.Adapter.Send(ctx, msg)
	if err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("send failed: %s", result.Error)
	}
	return nil
}
