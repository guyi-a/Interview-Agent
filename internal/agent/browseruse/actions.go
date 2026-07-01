package browseruse

import (
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

func (s *Session) Click(pageID string, index int) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	el, err := s.elementByIndex(pageID, index)
	if err != nil {
		return err
	}
	return locator(e.page, el).Click()
}

func (s *Session) Type(pageID string, index int, text string) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	el, err := s.elementByIndex(pageID, index)
	if err != nil {
		return err
	}
	loc := locator(e.page, el)
	if err := loc.Fill(""); err != nil {
		return fmt.Errorf("clear before type: %w", err)
	}
	return loc.Fill(text)
}

// Press dispatches a key event on the page (or on an element if index > 0).
// Common keys: "Enter", "Escape", "Tab", "ArrowDown", "Control+A".
func (s *Session) Press(pageID, key string, index int) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	if index <= 0 {
		return e.page.Keyboard().Press(key)
	}
	el, err := s.elementByIndex(pageID, index)
	if err != nil {
		return err
	}
	return locator(e.page, el).Press(key)
}

// locator turns the server-side selector (from snapshotJS) into a
// playwright Locator. `xpath=...` selectors use the xpath engine; others
// use the default css engine.
func locator(page playwright.Page, el *Element) playwright.Locator {
	if strings.HasPrefix(el.Selector, "xpath=") {
		return page.Locator(el.Selector)
	}
	return page.Locator(el.Selector)
}
