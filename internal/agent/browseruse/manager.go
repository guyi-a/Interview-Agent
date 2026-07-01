// Package browseruse drives a real browser (Chromium via playwright-go)
// from the agent's ReAct loop. One conversation gets one browser context.
package browseruse

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/playwright-community/playwright-go"
)

type Config struct {
	Headless bool
	SlowMoMS float64

	// Channel picks a specific chromium build:
	//   ""        — download and use playwright's bundled chromium
	//   "chrome"  — reuse the system Chrome install (no chromium download,
	//               but the playwright driver is still required)
	//   "msedge"  — likewise, Microsoft Edge
	Channel string
}

type Manager struct {
	cfg Config

	mu       sync.Mutex
	pw       *playwright.Playwright
	browser  playwright.Browser
	sessions map[string]*Session
}

func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg, sessions: make(map[string]*Session)}
}

// Session returns (creating if needed) the browser session for convID.
// First call boots Playwright + Chromium; blocks until ready.
// Returns ErrDriverMissing when the driver or chromium binary is absent.
func (m *Manager) Session(ctx context.Context, convID string) (*Session, error) {
	if convID == "" {
		return nil, errors.New("browseruse: convID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[convID]; ok {
		return s, nil
	}

	if err := m.ensureBrowserLocked(ctx); err != nil {
		return nil, err
	}

	bctx, err := m.browser.NewContext()
	if err != nil {
		return nil, fmt.Errorf("browseruse: NewContext: %w", err)
	}
	// Capture convID in the callback so CloseSession finds this exact
	// entry — the session key can't shift, but re-reading it inside the
	// closure keeps it explicit.
	cid := convID
	s := newSession(cid, bctx, func() { m.CloseSession(cid) })
	m.sessions[convID] = s
	return s, nil
}

func (m *Manager) CloseSession(convID string) {
	m.mu.Lock()
	s, ok := m.sessions[convID]
	if ok {
		delete(m.sessions, convID)
	}
	// When the last session goes away, tear the browser down too so the
	// Chromium main process actually exits. Next Session() call will
	// lazily boot Playwright + Chromium again.
	shutdownBrowser := len(m.sessions) == 0
	browser := m.browser
	pw := m.pw
	if shutdownBrowser {
		m.browser = nil
		m.pw = nil
	}
	m.mu.Unlock()

	if ok {
		if err := s.close(); err != nil {
			log.Printf("browseruse: close session %s: %v", convID, err)
		}
	}
	if shutdownBrowser {
		if browser != nil {
			_ = browser.Close()
		}
		if pw != nil {
			_ = pw.Stop()
		}
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = make(map[string]*Session)
	browser := m.browser
	pw := m.pw
	m.browser = nil
	m.pw = nil
	m.mu.Unlock()

	for id, s := range sessions {
		if err := s.close(); err != nil {
			log.Printf("browseruse: close session %s: %v", id, err)
		}
	}
	if browser != nil {
		_ = browser.Close()
	}
	if pw != nil {
		_ = pw.Stop()
	}
}

// ErrDriverMissing lets the tool layer route missing-driver failures into a
// "run browser_use_install" hint instead of a raw error dump.
var ErrDriverMissing = errors.New("browseruse: playwright driver or chromium not installed; run browser_use_install first")

func (m *Manager) ensureBrowserLocked(_ context.Context) error {
	if m.browser != nil {
		return nil
	}

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDriverMissing, err)
	}

	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(m.cfg.Headless),
	}
	if m.cfg.SlowMoMS > 0 {
		launchOpts.SlowMo = playwright.Float(m.cfg.SlowMoMS)
	}
	if m.cfg.Channel != "" {
		launchOpts.Channel = playwright.String(m.cfg.Channel)
	}

	browser, err := pw.Chromium.Launch(launchOpts)
	if err != nil {
		_ = pw.Stop()
		return fmt.Errorf("%w: %v", ErrDriverMissing, err)
	}

	m.pw = pw
	m.browser = browser
	return nil
}
