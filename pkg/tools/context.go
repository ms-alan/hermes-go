package tools

import (
	"context"
	"sync/atomic"

	"github.com/nousresearch/hermes-go/pkg/cron"
)

// deliveryOriginContextKey is the context key for the delivery origin.
type deliveryOriginContextKey struct{}

// currentDeliveryOrigin stores the origin for the currently-executing tool call.
// It is set by bridge.go before calling the handler and cleared after.
// This allows tools (e.g. cron) that don't receive context to still access
// the delivery origin of the current session.
var currentDeliveryOrigin atomic.Value // stores *cron.DeliveryOrigin

// WithDeliveryOrigin attaches a delivery origin to a context.
func WithDeliveryOrigin(ctx context.Context, origin *cron.DeliveryOrigin) context.Context {
	return context.WithValue(ctx, deliveryOriginContextKey{}, origin)
}

// DeliveryOriginFromContext retrieves the delivery origin from a context.
// Returns nil if not set.
func DeliveryOriginFromContext(ctx context.Context) *cron.DeliveryOrigin {
	val := ctx.Value(deliveryOriginContextKey{})
	if val == nil {
		return nil
	}
	return val.(*cron.DeliveryOrigin)
}

// SetCurrentDeliveryOrigin stores the origin for the in-flight tool call.
// Called by bridge.go before invoking the tool handler.
func SetCurrentDeliveryOrigin(origin *cron.DeliveryOrigin) {
	currentDeliveryOrigin.Store(origin)
}

// GetCurrentDeliveryOrigin returns the origin for the current tool call.
func GetCurrentDeliveryOrigin() *cron.DeliveryOrigin {
	val := currentDeliveryOrigin.Load()
	if val == nil {
		return nil
	}
	return val.(*cron.DeliveryOrigin)
}
