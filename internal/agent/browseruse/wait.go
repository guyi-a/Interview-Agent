package browseruse

import (
	"errors"
	"fmt"

	"github.com/playwright-community/playwright-go"
)

// Scroll moves the viewport by (dx, dy) pixels. Positive dy = down.
// If dx == 0 and dy == 0, this is a no-op (used to force a repaint /
// give async work a moment).
func (s *Session) Scroll(pageID string, dx, dy int) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	_, err = e.page.Evaluate(fmt.Sprintf("window.scrollBy(%d, %d)", dx, dy))
	return err
}

// WaitFor blocks until either:
//   - a css/text selector appears on the page, OR
//   - the given number of milliseconds passes (idle wait)
//
// Exactly one of selector / timeoutMS must be non-empty / non-zero.
func (s *Session) WaitFor(pageID, selector string, timeoutMS int) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	if selector == "" && timeoutMS <= 0 {
		return errors.New("wait_for: pass selector or timeout_ms")
	}
	if selector != "" {
		opts := playwright.PageWaitForSelectorOptions{}
		if timeoutMS > 0 {
			opts.Timeout = playwright.Float(float64(timeoutMS))
		}
		_, err := e.page.WaitForSelector(selector, opts)
		return err
	}
	e.page.WaitForTimeout(float64(timeoutMS))
	return nil
}
