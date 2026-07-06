package approval

import (
	"fmt"
	"sync"
)

// Mode names the per-conversation approval policy. The set is closed —
// unknown strings from the HTTP layer are rejected in Set / ValidMode.
type Mode string

const (
	// ModeDefault: policy.NeedsApproval decides. Every write/edit call
	// prompts the user. This is the safe default and the mode a fresh
	// conversation starts in.
	ModeDefault Mode = "default"
	// ModeAuto is UX-reserved for a future LLM classifier (krow-agent style:
	// destructive → deny, LLM allow → auto, otherwise → user). MVP: identical
	// to ModeDefault at runtime — the middleware log-warns once so the gap
	// is visible in server output.
	ModeAuto Mode = "auto"
	// ModeFullAccess: skip approval entirely. The middleware returns next()
	// without consulting policy.NeedsApproval. Session-scoped and dropped on
	// server restart so this "elevation" cannot outlive the process it was
	// granted in.
	ModeFullAccess Mode = "full_access"
)

func ValidMode(m Mode) bool {
	switch m {
	case ModeDefault, ModeAuto, ModeFullAccess:
		return true
	default:
		return false
	}
}

// ModeStore holds the per-conversation approval mode in memory. No DB
// persistence: an elevated mode ("full_access") should not silently survive
// a server restart — the user re-electing it after a restart is the audit
// trail we want.
//
// Read-heavy (middleware consults on every tool call), so we take a RWMutex
// and hand out RLock in Get.
type ModeStore struct {
	mu   sync.RWMutex
	byID map[string]Mode
}

func NewModeStore() *ModeStore {
	return &ModeStore{byID: make(map[string]Mode)}
}

// Get returns the mode for convID, or ModeDefault if the conversation has
// never explicitly chosen one (including after restart).
func (s *ModeStore) Get(convID string) Mode {
	if s == nil || convID == "" {
		return ModeDefault
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.byID[convID]; ok {
		return m
	}
	return ModeDefault
}

func (s *ModeStore) Set(convID string, m Mode) error {
	if s == nil {
		return fmt.Errorf("mode store not initialised")
	}
	if convID == "" {
		return fmt.Errorf("conversation id required")
	}
	if !ValidMode(m) {
		return fmt.Errorf("invalid mode %q", string(m))
	}
	s.mu.Lock()
	// Setting back to default is stored explicitly so a subsequent Get
	// doesn't fall through to the ModeDefault fallback branch and drop
	// the info that the user actively chose default (no observable diff
	// today, but keeps the store's semantics honest).
	s.byID[convID] = m
	s.mu.Unlock()
	return nil
}
