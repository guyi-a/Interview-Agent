package browseruse

import (
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

func (s *Session) Click(pageID string, index int) error {
	return s.clickVariant(pageID, index, "click")
}

func (s *Session) Hover(pageID string, index int) error {
	return s.clickVariant(pageID, index, "hover")
}

func (s *Session) Dblclick(pageID string, index int) error {
	return s.clickVariant(pageID, index, "dblclick")
}

func (s *Session) Rightclick(pageID string, index int) error {
	return s.clickVariant(pageID, index, "rightclick")
}

func (s *Session) clickVariant(pageID string, index int, kind string) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	el, err := s.elementByIndex(pageID, index)
	if err != nil {
		return err
	}
	loc := locator(e.page, el)
	switch kind {
	case "hover":
		return loc.Hover()
	case "dblclick":
		return loc.Dblclick()
	case "rightclick":
		return loc.Click(playwright.LocatorClickOptions{Button: playwright.MouseButtonRight})
	default:
		return loc.Click()
	}
}

func (s *Session) GoBack(pageID string) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	_, err = e.page.GoBack()
	return err
}

func (s *Session) Reload(pageID string) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	_, err = e.page.Reload()
	return err
}

// FocusPage brings the given tab to the front of its browser context so
// subsequent interactions target it. Handy when the model needs to switch
// between multiple open tabs.
func (s *Session) FocusPage(pageID string) error {
	e, err := s.page(pageID)
	if err != nil {
		return err
	}
	return e.page.BringToFront()
}

// ExecuteScript runs `script` in the page context and returns whatever it
// evaluates to. Script must be a JS expression or an async function body
// that returns a JSON-serialisable value.
func (s *Session) ExecuteScript(pageID, script string) (interface{}, error) {
	e, err := s.page(pageID)
	if err != nil {
		return nil, err
	}
	return e.page.Evaluate(script)
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
