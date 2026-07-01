package browserbridge

import (
	"context"
	"errors"
	"fmt"
)

// ExtensionStatus is what the tool layer returns from action=extension_status.
type ExtensionStatus struct {
	Ready             bool     `json:"ready"`
	SessionsCount     int      `json:"sessions_count"`
	BrowserIDs        []string `json:"browser_ids"`
	ExtensionVersions []string `json:"extension_versions,omitempty"`
}

func (s *Service) ExtensionStatus() ExtensionStatus {
	sessions := s.Registry.ListSessions()
	ids := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		ids = append(ids, sess.BrowserID)
	}
	return ExtensionStatus{
		Ready:             len(sessions) > 0,
		SessionsCount:     len(sessions),
		BrowserIDs:        ids,
		ExtensionVersions: s.Registry.ExtensionVersions(),
	}
}

func (s *Service) ListSessions() []BrowserSession { return s.Registry.ListSessions() }

func (s *Service) ListPages(browserID string) ([]BrowserPage, error) {
	if browserID == "" {
		return nil, errors.New("list_pages requires browser_id")
	}
	return s.Registry.ListPages(browserID), nil
}

// requirePage looks up the tab_id for a page_id so we can attach it to
// the arguments we send the extension. Missing → user-readable error.
func (s *Service) requirePage(browserID, pageID string) (*BrowserPage, error) {
	p := s.Registry.GetPage(browserID, pageID)
	if p == nil {
		return nil, fmt.Errorf("page_id %s not found under browser_id %s", pageID, browserID)
	}
	return p, nil
}

func (s *Service) OpenTab(ctx context.Context, browserID, url string, active bool) (map[string]interface{}, error) {
	if browserID == "" || url == "" {
		return nil, errors.New("open_tab requires browser_id and url")
	}
	return s.SendCommand(ctx, browserID, "browser_open_tab", map[string]interface{}{
		"browser_id": browserID,
		"url":        url,
		"active":     active,
	})
}

func (s *Service) FocusPage(ctx context.Context, browserID, pageID string) (map[string]interface{}, error) {
	p, err := s.requirePage(browserID, pageID)
	if err != nil {
		return nil, err
	}
	return s.SendCommand(ctx, browserID, "browser_focus_page", map[string]interface{}{
		"browser_id": browserID,
		"page_id":    pageID,
		"window_id":  p.WindowID,
		"tab_id":     p.TabID,
	})
}

func (s *Service) CloseTab(ctx context.Context, browserID, pageID string) (map[string]interface{}, error) {
	p, err := s.requirePage(browserID, pageID)
	if err != nil {
		return nil, err
	}
	return s.SendCommand(ctx, browserID, "browser_close_tab", map[string]interface{}{
		"browser_id": browserID,
		"page_id":    pageID,
		"tab_id":     p.TabID,
	})
}

func (s *Service) ReadState(ctx context.Context, browserID, pageID string) (map[string]interface{}, error) {
	p, err := s.requirePage(browserID, pageID)
	if err != nil {
		return nil, err
	}
	return s.SendCommand(ctx, browserID, "browser_read_state", map[string]interface{}{
		"browser_id": browserID,
		"page_id":    pageID,
		"tab_id":     p.TabID,
		"task_id":    "",
	})
}

// Click supports variant: click / dblclick / rightclick / hover. Extension
// dispatches on that field.
func (s *Service) Click(ctx context.Context, browserID, pageID string, index int, variant string) (map[string]interface{}, error) {
	p, err := s.requirePage(browserID, pageID)
	if err != nil {
		return nil, err
	}
	if variant == "" {
		variant = "click"
	}
	return s.SendCommand(ctx, browserID, "browser_click", map[string]interface{}{
		"browser_id": browserID,
		"page_id":    pageID,
		"tab_id":     p.TabID,
		"index":      index,
		"variant":    variant,
	})
}

func (s *Service) TypeText(ctx context.Context, browserID, pageID string, index int, text string) (map[string]interface{}, error) {
	p, err := s.requirePage(browserID, pageID)
	if err != nil {
		return nil, err
	}
	return s.SendCommand(ctx, browserID, "browser_type", map[string]interface{}{
		"browser_id": browserID,
		"page_id":    pageID,
		"tab_id":     p.TabID,
		"index":      index,
		"text":       text,
	})
}

func (s *Service) Press(ctx context.Context, browserID, pageID, key string, index int) (map[string]interface{}, error) {
	p, err := s.requirePage(browserID, pageID)
	if err != nil {
		return nil, err
	}
	args := map[string]interface{}{
		"browser_id": browserID,
		"page_id":    pageID,
		"tab_id":     p.TabID,
		"key":        key,
	}
	if index > 0 {
		args["index"] = index
	}
	return s.SendCommand(ctx, browserID, "browser_press", args)
}
