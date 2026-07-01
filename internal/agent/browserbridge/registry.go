package browserbridge

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

func nowMS() int64 { return time.Now().UnixMilli() }

// BridgeClient is one connected extension: a WebSocket session plus its
// self-reported browser identity.
type BridgeClient struct {
	SessionID        string
	BrowserID        string
	Label            string
	ExtensionVersion string
	ConnectedAt      int64
	LastSeenAt       int64
}

type Registry struct {
	mu               sync.Mutex
	InstanceID       string
	DiscoveryToken   string
	clients          map[string]*BridgeClient // session_id → client
	clientsByBrowser map[string]*BridgeClient // browser_id → client
	pages            map[string]*BrowserPage  // page_id → page
}

func NewRegistry() *Registry {
	return &Registry{
		InstanceID:       uuid.NewString(),
		DiscoveryToken:   uuid.NewString(),
		clients:          make(map[string]*BridgeClient),
		clientsByBrowser: make(map[string]*BridgeClient),
		pages:            make(map[string]*BrowserPage),
	}
}

func (r *Registry) PingPayload() BridgePingResponse {
	return BridgePingResponse{
		OK:              true,
		Server:          ServerName,
		InstanceID:      r.InstanceID,
		ProtocolVersion: ProtocolVersion,
		WSPath:          WSPath,
		Token:           r.DiscoveryToken,
	}
}

// RegisterClient allocates a session with a placeholder browser_id; the
// extension will overwrite that with its stable id via SetBrowserID once
// it sends the hello frame.
func (r *Registry) RegisterClient(label string) *BridgeClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := &BridgeClient{
		SessionID:   uuid.NewString(),
		BrowserID:   uuid.NewString(),
		Label:       label,
		ConnectedAt: nowMS(),
		LastSeenAt:  nowMS(),
	}
	r.clients[c.SessionID] = c
	r.clientsByBrowser[c.BrowserID] = c
	return c
}

func (r *Registry) UnregisterClient(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[sessionID]
	if !ok {
		return
	}
	delete(r.clients, sessionID)
	delete(r.clientsByBrowser, c.BrowserID)
	// Prune pages so subsequent list_pages doesn't return stale entries.
	for pid, p := range r.pages {
		if p.BrowserID == c.BrowserID {
			delete(r.pages, pid)
		}
	}
}

// SetBrowserID upgrades a client's temporary browser_id to the stable one
// the extension reports via hello. Returns the mutated client so callers
// can grab the updated fields.
func (r *Registry) SetBrowserID(sessionID, browserID, label, extVersion string) *BridgeClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[sessionID]
	if !ok {
		return nil
	}
	delete(r.clientsByBrowser, c.BrowserID)
	c.BrowserID = browserID
	if label != "" {
		c.Label = label
	}
	if extVersion != "" {
		c.ExtensionVersion = extVersion
	}
	c.LastSeenAt = nowMS()
	r.clientsByBrowser[browserID] = c
	return c
}

func (r *Registry) ClientByBrowser(browserID string) *BridgeClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clientsByBrowser[browserID]
}

func (r *Registry) ListSessions() []BrowserSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BrowserSession, 0, len(r.clients))
	for _, c := range r.clients {
		out = append(out, BrowserSession{
			BrowserID:  c.BrowserID,
			Label:      c.Label,
			LastSeenAt: c.LastSeenAt,
		})
	}
	return out
}

func (r *Registry) ExtensionVersions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, c := range r.clients {
		if c.ExtensionVersion != "" {
			out = append(out, c.ExtensionVersion)
		}
	}
	return out
}

// UpsertPage merges the page state pushed by a page_updated event.
// Deduplication is by (browser_id, tab_id); page_id is stable across
// updates so click / read_state calls can hold on to it.
func (r *Registry) UpsertPage(bid string, windowID, tabID int, url, title string, active bool, contextRole string) *BrowserPage {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pages {
		if p.BrowserID == bid && p.TabID == tabID {
			p.WindowID = windowID
			p.URL = url
			p.Title = title
			p.Active = active
			p.ContextRole = contextRole
			p.LastSeenAt = nowMS()
			return p
		}
	}
	p := &BrowserPage{
		PageID:       "page_" + uuid.NewString()[:8],
		BrowserID:    bid,
		WindowID:     windowID,
		TabID:        tabID,
		URL:          url,
		Title:        title,
		Active:       active,
		Controllable: true,
		ContextRole:  contextRole,
		LastSeenAt:   nowMS(),
	}
	r.pages[p.PageID] = p
	return p
}

func (r *Registry) RemovePage(bid string, tabID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid, p := range r.pages {
		if p.BrowserID == bid && p.TabID == tabID {
			delete(r.pages, pid)
		}
	}
}

func (r *Registry) GetPage(bid, pageID string) *BrowserPage {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pages[pageID]
	if !ok || p.BrowserID != bid {
		return nil
	}
	return p
}

func (r *Registry) ListPages(bid string) []BrowserPage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []BrowserPage{}
	for _, p := range r.pages {
		if p.BrowserID == bid {
			out = append(out, *p)
		}
	}
	return out
}
