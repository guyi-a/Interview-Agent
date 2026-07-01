package browseruse

import (
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/playwright-community/playwright-go"
)

type Session struct {
	convID string

	mu    sync.Mutex
	bctx  playwright.BrowserContext
	pages map[string]*pageEntry

	// onEmpty fires (outside the lock) after CloseTab drains the last page
	// so the Manager can tear down the browser context — otherwise a
	// user-closed browser session leaks its Chromium context in the pool.
	onEmpty func()
}

// pageEntry couples a live Page with the last read_state's element index.
// Actions like click(index=N) resolve N against snapshot.
type pageEntry struct {
	id       string
	page     playwright.Page
	snapshot []Element
}

func newSession(convID string, bctx playwright.BrowserContext, onEmpty func()) *Session {
	return &Session{
		convID:  convID,
		bctx:    bctx,
		pages:   make(map[string]*pageEntry),
		onEmpty: onEmpty,
	}
}

// OpenTab navigates to url in a new tab and returns page metadata.
// The returned page_id is what the LLM uses for follow-up actions.
func (s *Session) OpenTab(url string) (PageInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	page, err := s.bctx.NewPage()
	if err != nil {
		return PageInfo{}, fmt.Errorf("NewPage: %w", err)
	}
	if _, err := page.Goto(url); err != nil {
		_ = page.Close()
		return PageInfo{}, fmt.Errorf("Goto %s: %w", url, err)
	}
	title, _ := page.Title()

	entry := &pageEntry{id: "page_" + uuid.NewString()[:8], page: page}
	s.pages[entry.id] = entry
	return PageInfo{PageID: entry.id, URL: page.URL(), Title: title}, nil
}

func (s *Session) ListPages() []PageInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]PageInfo, 0, len(s.pages))
	for _, e := range s.pages {
		title, _ := e.page.Title()
		out = append(out, PageInfo{PageID: e.id, URL: e.page.URL(), Title: title})
	}
	return out
}

func (s *Session) CloseTab(pageID string) error {
	s.mu.Lock()
	e, ok := s.pages[pageID]
	if !ok {
		s.mu.Unlock()
		return ErrPageNotFound
	}
	delete(s.pages, pageID)
	empty := len(s.pages) == 0
	cb := s.onEmpty
	s.mu.Unlock()

	err := e.page.Close()
	if empty && cb != nil {
		cb()
	}
	return err
}

// page returns the pageEntry under session lock and releases the lock before
// returning — callers can then invoke Playwright ops without holding s.mu.
func (s *Session) page(pageID string) (*pageEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.pages[pageID]
	if !ok {
		return nil, ErrPageNotFound
	}
	return e, nil
}

func (s *Session) close() error {
	s.mu.Lock()
	pages := s.pages
	s.pages = map[string]*pageEntry{}
	bctx := s.bctx
	s.bctx = nil
	s.mu.Unlock()

	for _, e := range pages {
		_ = e.page.Close()
	}
	if bctx != nil {
		return bctx.Close()
	}
	return nil
}

var ErrPageNotFound = errors.New("browseruse: page_id not found")

type PageInfo struct {
	PageID string `json:"page_id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

// Element is one entry in a page's read_state snapshot. The `Selector`
// isn't returned to the LLM — it stays server-side so subsequent action
// calls (click/type/press) can resolve an index without re-walking DOM.
type Element struct {
	Index     int    `json:"index"`
	Tag       string `json:"tag"`
	Text      string `json:"text,omitempty"`
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	AriaLabel string `json:"aria_label,omitempty"`
	Href      string `json:"href,omitempty"`
	Name      string `json:"name,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Selector  string `json:"-"`
}
