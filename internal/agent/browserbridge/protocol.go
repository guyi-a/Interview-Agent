// Package browserbridge implements the server side of the Chrome
// extension bridge — a WebSocket channel through which a browser
// extension drives the user's real Chrome.
package browserbridge

import (
	"crypto/sha256"
	"encoding/hex"
)

// These constants are hardcoded on the extension side; changing any of
// them here means the extension can no longer connect.
const (
	PingClientID    = "kro-browser-bridge"
	PingSalt        = "kro-2026"
	PingSigHeader   = "X-Kro-Client-Sig"
	PingPath        = "/chrome-bridge/ping"
	WSPath          = "/chrome-bridge/ws"
	ServerName      = "krow-browser-bridge"
	ProtocolVersion = "1.0"

	WSCloseAuthFailed = 4401
)

var PingSignature = func() string {
	sum := sha256.Sum256([]byte(PingClientID + ":" + PingSalt))
	return hex.EncodeToString(sum[:])
}()
