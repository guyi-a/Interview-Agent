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

// TestBrowserUse_Sprint2 exercises the second batch of actions: scroll,
// wait_for, extract, screenshot, go_back / reload, execute_script. Doesn't
// need any real network — everything runs against the same file:// fixture.
func TestBrowserUse_Sprint2(t *testing.T) {
	if st := browseruse.CheckInstall(); !st.DriverReady || !st.BrowsersReady {
		t.Skipf("skip: %s", st.Message)
	}

	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "sprint2.html")
	if err := os.WriteFile(htmlPath, []byte(sprint2HTML), 0o644); err != nil {
		t.Fatalf("write sprint2.html: %v", err)
	}
	pageURL := "file://" + htmlPath

	mgr := browseruse.NewManager(browseruse.Config{Headless: true})
	defer mgr.Shutdown()

	sess, err := mgr.Session(context.Background(), "test-sprint2")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	info, err := sess.OpenTab(pageURL)
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}

	if err := sess.Scroll(info.PageID, 0, 400); err != nil {
		t.Fatalf("Scroll: %v", err)
	}

	if err := sess.WaitFor(info.PageID, "#late", 3000); err != nil {
		t.Fatalf("WaitFor #late: %v", err)
	}

	state, err := sess.ReadState(info.PageID)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !strings.Contains(state, "late-button") {
		t.Fatalf("late-button not in state after wait_for:\n%s", state)
	}
	lateIdx := indexOfLineContaining(state, "late-button")
	if lateIdx == 0 {
		t.Fatalf("could not resolve late-button index")
	}

	text, _, err := sess.Extract(info.PageID, lateIdx, false)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(text, "late-button") {
		t.Fatalf("extracted text mismatch: %q", text)
	}

	shotPath := filepath.Join(dir, "shot.png")
	abs, b64, err := sess.Screenshot(info.PageID, shotPath, false)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if abs != shotPath {
		t.Fatalf("screenshot path mismatch: got %q want %q", abs, shotPath)
	}
	if b64 != "" {
		t.Errorf("screenshot with save_path should not return base64, got len=%d", len(b64))
	}
	fi, err := os.Stat(shotPath)
	if err != nil {
		t.Fatalf("screenshot file: %v", err)
	}
	if fi.Size() < 100 {
		t.Fatalf("screenshot too small: %d bytes", fi.Size())
	}

	val, err := sess.ExecuteScript(info.PageID, "() => document.title")
	if err != nil {
		t.Fatalf("ExecuteScript: %v", err)
	}
	if title, ok := val.(string); !ok || title != "sprint2 fixture" {
		t.Fatalf("execute_script returned unexpected value: %v", val)
	}

	if err := sess.Reload(info.PageID); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// After reload, #late reappears on the same delay — re-wait proves
	// the page reloaded rather than getting a cached snapshot.
	if err := sess.WaitFor(info.PageID, "#late", 3000); err != nil {
		t.Fatalf("WaitFor after reload: %v", err)
	}

	t.Logf("sprint2 actions all passed; screenshot=%s", shotPath)
}

const sprint2HTML = `<!doctype html>
<html><head><title>sprint2 fixture</title></head>
<body style="height:2000px">
  <h1>top of page</h1>
  <div style="height:800px"></div>
  <div id="anchor">middle</div>
  <script>
    setTimeout(() => {
      const b = document.createElement('button');
      b.id = 'late';
      b.innerText = 'late-button';
      document.body.appendChild(b);
    }, 200);
  </script>
</body></html>`

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
