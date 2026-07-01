package browseruse

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/playwright-community/playwright-go"
)

// Screenshot captures the given page. When savePath is empty, the PNG bytes
// are returned base64-encoded (short pages only — for anything real you
// should save to disk). Otherwise it writes to savePath and returns the
// absolute path.
func (s *Session) Screenshot(pageID, savePath string, fullPage bool) (path string, b64 string, err error) {
	e, err := s.page(pageID)
	if err != nil {
		return "", "", err
	}
	opts := playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(fullPage),
		Type:     playwright.ScreenshotTypePng,
	}
	buf, err := e.page.Screenshot(opts)
	if err != nil {
		return "", "", fmt.Errorf("screenshot: %w", err)
	}
	if savePath == "" {
		return "", base64.StdEncoding.EncodeToString(buf), nil
	}
	abs, err := filepath.Abs(savePath)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", savePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir %s: %w", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, buf, 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", abs, err)
	}
	return abs, "", nil
}

// Extract returns the visible text of the element at the given index. If
// includeHTML is true, the element's outerHTML is included as well.
// For attribute lookups the LLM should use execute_script instead.
func (s *Session) Extract(pageID string, index int, includeHTML bool) (text, html string, err error) {
	e, err := s.page(pageID)
	if err != nil {
		return "", "", err
	}
	el, err := s.elementByIndex(pageID, index)
	if err != nil {
		return "", "", err
	}
	loc := locator(e.page, el)
	txt, tErr := loc.TextContent()
	if tErr != nil {
		return "", "", fmt.Errorf("text: %w", tErr)
	}
	if !includeHTML {
		return txt, "", nil
	}
	// InnerHTML is enough for most inspection needs; outerHTML would double
	// the payload without much extra info for LLM decisions.
	h, hErr := loc.InnerHTML()
	if hErr != nil {
		return txt, "", nil
	}
	return txt, h, nil
}
