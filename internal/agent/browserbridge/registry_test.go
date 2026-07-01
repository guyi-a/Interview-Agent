package browserbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestPingSignatureMatchesSpec(t *testing.T) {
	want := sha256.Sum256([]byte("kro-browser-bridge:kro-2026"))
	got := PingSignature
	if hex.EncodeToString(want[:]) != got {
		t.Fatalf("PingSignature drifted from spec:\n got=%s\nwant=%s", got, hex.EncodeToString(want[:]))
	}
}

func TestRegistrySetBrowserIDReindexes(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterClient("test")
	tmpID := c.BrowserID
	if r.ClientByBrowser(tmpID) != c {
		t.Fatal("temp browser id should resolve before hello")
	}

	updated := r.SetBrowserID(c.SessionID, "stable-abc", "MyChrome", "1.2.3")
	if updated == nil {
		t.Fatal("SetBrowserID returned nil for known session")
	}
	if updated.BrowserID != "stable-abc" {
		t.Fatalf("browser id not updated: %q", updated.BrowserID)
	}
	if r.ClientByBrowser(tmpID) != nil {
		t.Fatal("old temp id should be removed from reverse index")
	}
	if r.ClientByBrowser("stable-abc") == nil {
		t.Fatal("stable id should resolve after hello")
	}
	if updated.ExtensionVersion != "1.2.3" || updated.Label != "MyChrome" {
		t.Fatalf("metadata not set: %+v", updated)
	}
}

func TestRegistryUnregisterPrunesPages(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterClient("test")
	r.SetBrowserID(c.SessionID, "stable", "L", "")

	p := r.UpsertPage("stable", 1, 42, "https://example.com", "example", true, "")
	if r.GetPage("stable", p.PageID) == nil {
		t.Fatal("page should be looked up before unregister")
	}

	r.UnregisterClient(c.SessionID)
	if r.ClientByBrowser("stable") != nil {
		t.Fatal("client should be gone after unregister")
	}
	if r.GetPage("stable", p.PageID) != nil {
		t.Fatal("pages belonging to disconnected client should be pruned")
	}
}

func TestRegistryUpsertPageDedupesByTabID(t *testing.T) {
	r := NewRegistry()
	first := r.UpsertPage("b1", 1, 7, "https://a", "A", true, "")
	second := r.UpsertPage("b1", 1, 7, "https://b", "B", true, "")
	if first.PageID != second.PageID {
		t.Fatalf("upsert should return same page_id for same (browser_id, tab_id); got %s vs %s", first.PageID, second.PageID)
	}
	if second.URL != "https://b" || second.Title != "B" {
		t.Fatalf("upsert should overwrite mutable fields; got %+v", second)
	}
}
