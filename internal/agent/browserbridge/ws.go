package browserbridge

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// wsUpgrader allows any Origin because the connection is initiated by a
// browser extension whose Origin is chrome-extension://<hash>, not a page
// we serve. Same policy as the reference implementation.
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Register wires the two extension-facing HTTP routes onto r. It does
// not use the /api prefix that our normal APIs use — the extension has
// these paths hardcoded.
func Register(r *gin.Engine, svc *Service) {
	r.GET(PingPath, func(c *gin.Context) { handlePing(c, svc) })
	r.HEAD(PingPath, func(c *gin.Context) { handlePing(c, svc) })
	r.GET(WSPath, func(c *gin.Context) { handleWS(c, svc) })
}

func handlePing(c *gin.Context, svc *Service) {
	// Only GET is signature-checked; HEAD is for liveness probes.
	if c.Request.Method == http.MethodGet {
		if c.GetHeader(PingSigHeader) != PingSignature {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid client signature"})
			return
		}
	}
	c.JSON(http.StatusOK, svc.Registry.PingPayload())
}

func handleWS(c *gin.Context, svc *Service) {
	token := c.Query("token")
	if token != svc.Registry.DiscoveryToken {
		conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(WSCloseAuthFailed, "auth failed"),
			timeAfterSecs(2),
		)
		_ = conn.Close()
		return
	}

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := svc.Registry.RegisterClient("Chrome Extension")
	svc.attachConn(client.SessionID, conn)
	log.Printf("chrome-bridge ws connected sid=%s", client.SessionID[:8])

	defer func() {
		svc.detachConn(client.SessionID)
		svc.Registry.UnregisterClient(client.SessionID)
		_ = conn.Close()
		log.Printf("chrome-bridge ws disconnected sid=%s", client.SessionID[:8])
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := decodeFrame(raw)
		if err != nil {
			continue
		}
		routeFrame(svc, client, msg)
	}
}

func routeFrame(svc *Service, client *BridgeClient, msg map[string]interface{}) {
	msgType, _ := msg["type"].(string)
	payload := framePayload(msg)

	// event frames re-classify into their subtype so page bookkeeping and
	// command replies live in the same switch.
	if msgType == "event" {
		if name, ok := payload["name"].(string); ok {
			switch name {
			case "page_updated":
				msgType = "page_updated"
			case "page_closed", "page_removed":
				msgType = "page_removed"
			}
		}
	}

	switch msgType {
	case "hello":
		handleHello(svc, client, msg, payload)
	case "page_updated":
		handlePageUpdated(svc, payload)
	case "page_removed":
		handlePageRemoved(svc, payload)
	case "result":
		if id, ok := msg["id"].(string); ok {
			svc.deliverResult(id, payload)
		}
	case "error":
		if id, ok := msg["id"].(string); ok {
			detail, _ := payload["message"].(string)
			if code, ok := payload["code"].(string); ok && code != "" {
				detail = code + ": " + detail
			}
			if detail == "" {
				detail = "browser bridge command failed"
			}
			svc.deliverError(id, errFromExtension(detail))
		}
	case "ping":
		// heartbeat, ignore
	}
}

func handleHello(svc *Service, client *BridgeClient, msg, payload map[string]interface{}) {
	browserID, _ := payload["browser_id"].(string)
	browserLabel, _ := payload["browser_label"].(string)
	extVersion := ""
	if ci, ok := payload["client"].(map[string]interface{}); ok {
		extVersion, _ = ci["extension_version"].(string)
	}
	if browserID != "" {
		if updated := svc.Registry.SetBrowserID(client.SessionID, browserID, browserLabel, extVersion); updated != nil {
			*client = *updated
		}
	}
	id, _ := msg["id"].(string)
	if id == "" {
		id = "hello_ack"
	}
	_ = svc.writeJSON(client.SessionID, map[string]interface{}{
		"type":       "hello_ack",
		"id":         id,
		"session_id": client.SessionID,
		"timestamp":  nowMS(),
		"payload": map[string]interface{}{
			"browser_id":    client.BrowserID,
			"browser_label": client.Label,
		},
	})
	log.Printf("chrome-bridge hello sid=%s browser_id=%s version=%s",
		client.SessionID[:8], safePrefix(client.BrowserID, 8), extVersion)
}

func handlePageUpdated(svc *Service, payload map[string]interface{}) {
	bid, _ := payload["browser_id"].(string)
	tabID, tabOK := payload["tab_id"].(float64) // JSON numbers arrive as float64
	windowID, winOK := payload["window_id"].(float64)
	if bid == "" || !tabOK || !winOK {
		return
	}
	url, _ := payload["url"].(string)
	title, _ := payload["title"].(string)
	active, _ := payload["active"].(bool)
	ctxRole, _ := payload["context_role"].(string)
	svc.Registry.UpsertPage(bid, int(windowID), int(tabID), url, title, active, ctxRole)
}

func handlePageRemoved(svc *Service, payload map[string]interface{}) {
	bid, _ := payload["browser_id"].(string)
	tabID, tabOK := payload["tab_id"].(float64)
	if bid == "" || !tabOK {
		return
	}
	svc.Registry.RemovePage(bid, int(tabID))
}
