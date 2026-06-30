package contextkey

import (
	"context"

	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ctxKey string

const (
	conversationIDKey ctxKey = "conversation_id"
	bufferKey         ctxKey = "stream_buffer"
)

func WithConversationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, conversationIDKey, id)
}

func ConversationID(ctx context.Context) string {
	v, _ := ctx.Value(conversationIDKey).(string)
	return v
}

func WithBuffer(ctx context.Context, buf *stream.StreamBuffer) context.Context {
	return context.WithValue(ctx, bufferKey, buf)
}

func Buffer(ctx context.Context) *stream.StreamBuffer {
	v, _ := ctx.Value(bufferKey).(*stream.StreamBuffer)
	return v
}
