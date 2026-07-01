package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
)

// TestBrowserUse_OpenReadClick verifies the core loop:
//   open_tab(file://…/testpage.html)
//   read_state → markdown mentions "Sign in" button
//   click(index=N) where N is that button
//   read_state → page reflects the click
//
// Skipped when Chromium isn't installed. Run once locally after:
//   go test -run TestBrowserUseInstall ./test
// or manually via `playwright install chromium`.
func TestBrowserUse_OpenReadClick(t *testing.T) {
	if st := browseruse.CheckInstall(); !st.DriverReady || !st.BrowsersReady {
		t.Skipf("skip: %s", st.Message)
	}

	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "testpage.html")
	if err := os.WriteFile(htmlPath, []byte(testPageHTML), 0o644); err != nil {
		t.Fatalf("write testpage.html: %v", err)
	}
	pageURL := "file://" + htmlPath

	mgr := browseruse.NewManager(browseruse.Config{Headless: true})
	defer mgr.Shutdown()

	ctx := context.Background()
	sess, err := mgr.Session(ctx, "test-conv")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}

	info, err := sess.OpenTab(pageURL)
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	t.Logf("opened page_id=%s url=%s title=%q", info.PageID, info.URL, info.Title)

	state, err := sess.ReadState(info.PageID)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	t.Logf("=== state ===\n%s", state)

	if !strings.Contains(state, "Sign in") {
		t.Fatalf("state markdown missing 'Sign in' button:\n%s", state)
	}
	if !strings.Contains(state, "search") {
		t.Fatalf("state markdown missing 'search' input:\n%s", state)
	}

	signInIdx := indexOfLineContaining(state, "Sign in")
	if signInIdx == 0 {
		t.Fatal("could not resolve Sign in index from state markdown")
	}
	if err := sess.Click(info.PageID, signInIdx); err != nil {
		t.Fatalf("Click(Sign in): %v", err)
	}

	after, err := sess.ReadState(info.PageID)
	if err != nil {
		t.Fatalf("ReadState after click: %v", err)
	}
	t.Logf("=== state after click ===\n%s", after)

	if !strings.Contains(after, "clicked!") {
		t.Fatalf("page did not reflect click — expected label 'clicked!' in state:\n%s", after)
	}
}

// TestBrowserUse_TypePress verifies type + press flow: fill an input, press Enter.
func TestBrowserUse_TypePress(t *testing.T) {
	if st := browseruse.CheckInstall(); !st.DriverReady || !st.BrowsersReady {
		t.Skipf("skip: %s", st.Message)
	}

	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "testpage.html")
	if err := os.WriteFile(htmlPath, []byte(testPageHTML), 0o644); err != nil {
		t.Fatalf("write testpage.html: %v", err)
	}
	pageURL := "file://" + htmlPath

	mgr := browseruse.NewManager(browseruse.Config{Headless: true})
	defer mgr.Shutdown()

	sess, err := mgr.Session(context.Background(), "test-conv-2")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	info, err := sess.OpenTab(pageURL)
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	state, err := sess.ReadState(info.PageID)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}

	inputIdx := indexOfLineContaining(state, `placeholder="search"`)
	if inputIdx == 0 {
		t.Fatalf("no search input in state:\n%s", state)
	}
	if err := sess.Type(info.PageID, inputIdx, "hello world"); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := sess.Press(info.PageID, "Enter", inputIdx); err != nil {
		t.Fatalf("Press Enter: %v", err)
	}

	after, err := sess.ReadState(info.PageID)
	if err != nil {
		t.Fatalf("ReadState after: %v", err)
	}
	t.Logf("=== state after type + Enter ===\n%s", after)

	if !strings.Contains(after, "submitted:hello world") {
		t.Fatalf("submit label not updated:\n%s", after)
	}
}

// indexOfLineContaining scans lines like "[7] <button>  Sign in" and returns
// the leading number when substr appears anywhere on that line. Returns 0
// when no line matches.
func indexOfLineContaining(s, substr string) int {
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, substr) {
			continue
		}
		if !strings.HasPrefix(line, "[") {
			continue
		}
		end := strings.Index(line, "]")
		if end < 0 {
			continue
		}
		var n int
		if _, err := parseIntPrefix(line[1:end], &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func parseIntPrefix(s string, out *int) (int, error) {
	n := 0
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	if i == 0 {
		return 0, os.ErrInvalid
	}
	return i, nil
}

// The click test reads state off the button's own innerText change.
// The type/press test hangs its assertion on a sibling readonly input that
// gets populated by the search box's onkeydown Enter handler — read_state
// walks input.value, so the "submitted:…" string shows up in state markdown.
const testPageHTML = `<!doctype html>
<html><body>
  <h1>test page</h1>
  <button id="signin" onclick="this.innerText='clicked!'">Sign in</button>
  <input name="q" placeholder="search"
    onkeydown="if(event.key==='Enter'){event.preventDefault(); document.getElementById('out').value='submitted:'+this.value;}" />
  <input id="out" name="out" readonly />
</body></html>`
