package browserbridge

type BridgePingResponse struct {
	OK              bool   `json:"ok"`
	Server          string `json:"server"`
	InstanceID      string `json:"instanceId"`
	ProtocolVersion string `json:"protocolVersion"`
	WSPath          string `json:"wsPath"`
	Token           string `json:"token"`
}

type BrowserSession struct {
	BrowserID  string `json:"browser_id"`
	Label      string `json:"label"`
	LastSeenAt int64  `json:"last_seen_at"`
}

type BrowserPage struct {
	PageID       string `json:"page_id"`
	BrowserID    string `json:"browser_id"`
	WindowID     int    `json:"window_id"`
	TabID        int    `json:"tab_id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Active       bool   `json:"active"`
	Controllable bool   `json:"controllable"`
	ContextRole  string `json:"context_role,omitempty"`
	LastSeenAt   int64  `json:"last_seen_at"`
}

type CommandEnvelope struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Timestamp int64          `json:"timestamp"`
	Payload   CommandPayload `json:"payload"`
}

type CommandPayload struct {
	Tool      string                 `json:"tool"`
	Arguments map[string]interface{} `json:"arguments"`
}
