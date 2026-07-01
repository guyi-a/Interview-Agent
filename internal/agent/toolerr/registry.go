package toolerr

import (
	"context"
	"sync"
)

// Registry tracks which tool calls in the current agent run failed and what
// error message the middleware swallowed. SSE handlers consult it to decide
// whether to emit `ok=true` (real success) or `ok=false` (middleware-rescued
// failure) on the corresponding tool_result frame.
//
// One Registry per agent run. Created in ChatService.runAgent and attached
// to the run ctx via WithRegistry.
type Registry struct {
	mu sync.Mutex
	m  map[string]string // tool_call_id -> sanitized error message
}

func NewRegistry() *Registry {
	return &Registry{m: make(map[string]string)}
}

// Record stores the error message for a failed tool call. No-op on a nil
// receiver so middleware code stays clean when no Registry is attached.
func (r *Registry) Record(callID, errMsg string) {
	if r == nil || callID == "" {
		return
	}
	r.mu.Lock()
	r.m[callID] = errMsg
	r.mu.Unlock()
}

// Lookup reports whether the given tool call failed, returning the recorded
// error message. Safe to call on a nil receiver.
func (r *Registry) Lookup(callID string) (string, bool) {
	if r == nil || callID == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	msg, ok := r.m[callID]
	return msg, ok
}

type ctxKey struct{}

// WithRegistry attaches a Registry to ctx so middleware and downstream
// consumers can find it via FromContext.
func WithRegistry(ctx context.Context, r *Registry) context.Context {
	return context.WithValue(ctx, ctxKey{}, r)
}

// FromContext returns the Registry attached to ctx, or nil if none.
func FromContext(ctx context.Context) *Registry {
	r, _ := ctx.Value(ctxKey{}).(*Registry)
	return r
}
