package browseruse

import (
	"encoding/json"
	"fmt"
	"strings"
)

// snapshotJS runs in the page and returns the interactive elements ordered
// by document position, filtered to visible / on-screen. Each element gets
// a stable-ish selector (id > data-testid > name > xpath fallback) so the
// server side can turn "click index N" into a locator.
const snapshotJS = `() => {
  const SELECTORS = 'a, button, input, textarea, select, [role="button"], [role="link"], [role="tab"], [role="menuitem"], [contenteditable="true"]';
  const nodes = Array.from(document.querySelectorAll(SELECTORS));
  const isVisible = (el) => {
    const rect = el.getBoundingClientRect();
    if (rect.width < 2 || rect.height < 2) return false;
    const style = window.getComputedStyle(el);
    if (style.visibility === 'hidden' || style.display === 'none') return false;
    if (parseFloat(style.opacity) === 0) return false;
    return true;
  };
  const xpath = (el) => {
    const parts = [];
    let node = el;
    while (node && node.nodeType === 1 && node !== document.body) {
      let i = 1;
      let sib = node.previousElementSibling;
      while (sib) { if (sib.tagName === node.tagName) i++; sib = sib.previousElementSibling; }
      parts.unshift(node.tagName.toLowerCase() + '[' + i + ']');
      node = node.parentElement;
    }
    return '/html/body/' + parts.join('/');
  };
  const trim = (s) => (s || '').replace(/\s+/g, ' ').trim().slice(0, 120);
  const selectorOf = (el) => {
    if (el.id && /^[a-zA-Z_][\w-]*$/.test(el.id)) return '#' + el.id;
    const tid = el.getAttribute('data-testid');
    if (tid) return '[data-testid="' + tid.replace(/"/g, '\\"') + '"]';
    const name = el.getAttribute('name');
    if (name && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT')) {
      return el.tagName.toLowerCase() + '[name="' + name.replace(/"/g, '\\"') + '"]';
    }
    return 'xpath=' + xpath(el);
  };
  return {
    title: document.title,
    url: location.href,
    nodes: nodes.filter(isVisible).map((el) => ({
      tag: el.tagName.toLowerCase(),
      text: trim(el.innerText || el.value || ''),
      type: el.getAttribute('type') || '',
      role: el.getAttribute('role') || '',
      aria_label: el.getAttribute('aria-label') || '',
      href: el.getAttribute('href') || '',
      name: el.getAttribute('name') || '',
      placeholder: el.getAttribute('placeholder') || '',
      selector: selectorOf(el),
    })),
  };
}`

type snapshotResult struct {
	Title string        `json:"title"`
	URL   string        `json:"url"`
	Nodes []rawSnapshot `json:"nodes"`
}

type rawSnapshot struct {
	Tag         string `json:"tag"`
	Text        string `json:"text"`
	Type        string `json:"type"`
	Role        string `json:"role"`
	AriaLabel   string `json:"aria_label"`
	Href        string `json:"href"`
	Name        string `json:"name"`
	Placeholder string `json:"placeholder"`
	Selector    string `json:"selector"`
}

// ReadState walks the current DOM of pageID and returns a numbered markdown
// dump of the interactive elements plus page title / URL. The snapshot is
// cached on the pageEntry so a subsequent Click(index=N) etc. can resolve
// N without re-walking the tree.
func (s *Session) ReadState(pageID string) (string, error) {
	e, err := s.page(pageID)
	if err != nil {
		return "", err
	}

	raw, err := e.page.Evaluate(snapshotJS)
	if err != nil {
		return "", fmt.Errorf("evaluate: %w", err)
	}

	buf, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}
	var snap snapshotResult
	if err := json.Unmarshal(buf, &snap); err != nil {
		return "", fmt.Errorf("unmarshal snapshot: %w", err)
	}

	elems := make([]Element, len(snap.Nodes))
	for i, n := range snap.Nodes {
		elems[i] = Element{
			Index:       i + 1,
			Tag:         n.Tag,
			Text:        n.Text,
			Type:        n.Type,
			Role:        n.Role,
			AriaLabel:   n.AriaLabel,
			Href:        n.Href,
			Name:        n.Name,
			Placeholder: n.Placeholder,
			Selector:    n.Selector,
		}
	}

	s.mu.Lock()
	e.snapshot = elems
	s.mu.Unlock()

	return renderStateMarkdown(snap.Title, snap.URL, elems), nil
}

// elementByIndex returns the cached snapshot entry for the given index, or
// nil if the index is out of range / no read_state has run yet.
func (s *Session) elementByIndex(pageID string, index int) (*Element, error) {
	e, err := s.page(pageID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(e.snapshot) == 0 {
		return nil, fmt.Errorf("index %d 无效：先调 read_state 生成新快照", index)
	}
	if index < 1 || index > len(e.snapshot) {
		return nil, fmt.Errorf("index %d 越界：当前快照只有 %d 项，先调 read_state 拿新 index", index, len(e.snapshot))
	}
	el := e.snapshot[index-1]
	return &el, nil
}

func renderStateMarkdown(title, url string, elems []Element) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\nURL: %s\n\n", title, url)
	if len(elems) == 0 {
		b.WriteString("(no interactive elements)\n")
		return b.String()
	}
	for _, el := range elems {
		b.WriteString(formatElement(el))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatElement(el Element) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] <%s>", el.Index, el.Tag)

	attrs := []string{}
	if el.Type != "" {
		attrs = append(attrs, "type="+el.Type)
	}
	if el.Role != "" {
		attrs = append(attrs, "role="+el.Role)
	}
	if el.Name != "" {
		attrs = append(attrs, "name="+el.Name)
	}
	if el.Href != "" {
		attrs = append(attrs, "href="+el.Href)
	}
	if el.Placeholder != "" {
		attrs = append(attrs, `placeholder="`+el.Placeholder+`"`)
	}
	if el.AriaLabel != "" {
		attrs = append(attrs, `aria-label="`+el.AriaLabel+`"`)
	}
	if len(attrs) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(attrs, " "))
	}
	if el.Text != "" {
		fmt.Fprintf(&b, "  %s", el.Text)
	}
	return b.String()
}
