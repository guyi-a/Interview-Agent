package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const defaultCommandTimeout = 30 * time.Second

// pendingResult carries either the JSON payload of a successful result or
// an error that should surface to the caller. Set exactly once.
type pendingResult struct {
	payload map[string]interface{}
	err     error
}

type Service struct {
	Registry *Registry

	mu          sync.Mutex
	connections map[string]*websocket.Conn // session_id → conn
	writeLocks  map[string]*sync.Mutex     // gorilla websocket writes aren't concurrent-safe
	pending     map[string]chan pendingResult
}

func NewService(reg *Registry) *Service {
	return &Service{
		Registry:    reg,
		connections: make(map[string]*websocket.Conn),
		writeLocks:  make(map[string]*sync.Mutex),
		pending:     make(map[string]chan pendingResult),
	}
}

func (s *Service) attachConn(sessionID string, conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections[sessionID] = conn
	s.writeLocks[sessionID] = &sync.Mutex{}
}

func (s *Service) detachConn(sessionID string) {
	s.mu.Lock()
	delete(s.connections, sessionID)
	delete(s.writeLocks, sessionID)
	// Fail any in-flight commands so callers stop blocking.
	pending := s.pending
	s.pending = make(map[string]chan pendingResult)
	s.mu.Unlock()

	for id, ch := range pending {
		ch <- pendingResult{err: fmt.Errorf("browser websocket disconnected mid-command %s", id)}
		close(ch)
	}
}

func (s *Service) writeJSON(sessionID string, v interface{}) error {
	s.mu.Lock()
	conn := s.connections[sessionID]
	lock := s.writeLocks[sessionID]
	s.mu.Unlock()
	if conn == nil {
		return errors.New("no active websocket for session")
	}
	lock.Lock()
	defer lock.Unlock()
	return conn.WriteJSON(v)
}

// deliverResult / deliverError are called from the receive loop when the
// extension replies to a previously-sent command.
func (s *Service) deliverResult(cmdID string, payload map[string]interface{}) {
	s.mu.Lock()
	ch, ok := s.pending[cmdID]
	if ok {
		delete(s.pending, cmdID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	ch <- pendingResult{payload: payload}
	close(ch)
}

func (s *Service) deliverError(cmdID string, err error) {
	s.mu.Lock()
	ch, ok := s.pending[cmdID]
	if ok {
		delete(s.pending, cmdID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	ch <- pendingResult{err: err}
	close(ch)
}

// SendCommand ships one command to the extension owning browserID and
// blocks until the matching result arrives, ctx expires, or a default
// timeout kicks in.
func (s *Service) SendCommand(ctx context.Context, browserID, tool string, args map[string]interface{}) (map[string]interface{}, error) {
	client := s.Registry.ClientByBrowser(browserID)
	if client == nil {
		return nil, fmt.Errorf("browser_id %s not connected", browserID)
	}

	cmdID := uuid.NewString()
	ch := make(chan pendingResult, 1)

	s.mu.Lock()
	if _, exists := s.connections[client.SessionID]; !exists {
		s.mu.Unlock()
		return nil, errors.New("browser websocket not available")
	}
	s.pending[cmdID] = ch
	s.mu.Unlock()

	envelope := CommandEnvelope{
		Type:      "command",
		ID:        cmdID,
		SessionID: client.SessionID,
		Timestamp: nowMS(),
		Payload: CommandPayload{
			Tool:      tool,
			Arguments: args,
		},
	}
	if err := s.writeJSON(client.SessionID, envelope); err != nil {
		s.mu.Lock()
		delete(s.pending, cmdID)
		s.mu.Unlock()
		return nil, fmt.Errorf("send command: %w", err)
	}

	deadline := time.After(defaultCommandTimeout)
	select {
	case res := <-ch:
		return res.payload, res.err
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, cmdID)
		s.mu.Unlock()
		return nil, ctx.Err()
	case <-deadline:
		s.mu.Lock()
		delete(s.pending, cmdID)
		s.mu.Unlock()
		return nil, fmt.Errorf("browser bridge command %s timed out after %s", tool, defaultCommandTimeout)
	}
}

// framePayload pulls "payload" from a decoded WS frame as map[string]any.
// Some frames come with null / non-object payload; we return empty map in
// that case so callers can safely index.
func framePayload(m map[string]interface{}) map[string]interface{} {
	p, ok := m["payload"].(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}
	return p
}

// helper for the receive loop: decode a raw WS frame, no strong typing —
// the format is loose enough that map[string]any is friendlier.
func decodeFrame(raw []byte) (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
