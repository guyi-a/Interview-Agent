package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/browserbridge"
)

type browserBridgeInput struct {
	Action    string `json:"action" jsonschema:"description=One of: extension_status / list_sessions / list_pages / open_tab / focus_page / close_tab / read_state / click / type / press"`
	BrowserID string `json:"browser_id,omitempty" jsonschema:"description=Extension-reported stable id from list_sessions. Required for everything except extension_status / list_sessions."`
	PageID    string `json:"page_id,omitempty" jsonschema:"description=Page identifier from list_pages / open_tab. Required for focus_page / close_tab / read_state / click / type / press."`
	URL       string `json:"url,omitempty" jsonschema:"description=Absolute URL for open_tab."`
	Active    bool   `json:"active,omitempty" jsonschema:"description=For open_tab: whether the new tab should be foregrounded. Defaults true."`
	Index     int    `json:"index,omitempty" jsonschema:"description=Element index from the last read_state. Required for click / type; optional for press."`
	Variant   string `json:"variant,omitempty" jsonschema:"description=For click: click / dblclick / rightclick / hover. Defaults click."`
	Text      string `json:"text,omitempty" jsonschema:"description=For type: what to fill into the element."`
	Key       string `json:"key,omitempty" jsonschema:"description=For press: 'Enter' / 'Tab' / 'Escape' etc."`
}

type browserBridgeOutput struct {
	OK       bool                        `json:"ok"`
	Message  string                      `json:"message,omitempty"`
	Status   *browserbridge.ExtensionStatus `json:"status,omitempty"`
	Sessions []browserbridge.BrowserSession `json:"sessions,omitempty"`
	Pages    []browserbridge.BrowserPage    `json:"pages,omitempty"`
	Data     map[string]interface{}      `json:"data,omitempty"`
}

func newBrowserBridgeTool(svc *browserbridge.Service) (tool.BaseTool, error) {
	if svc == nil {
		return nil, errors.New("browser_bridge: service is nil")
	}
	desc := "Drive the user's own Chrome via the Kro Browser Bridge extension. " +
		"Requires the user to have the extension installed and connected — always start with " +
		"action='extension_status' to check. If ready=false, fall back to browser_use instead. " +
		"Typical flow: extension_status → list_sessions (pick browser_id) → list_pages / open_tab → " +
		"read_state (get index) → click/type/press by index → read_state again → ... . " +
		"Element indices only stay valid until the DOM changes; re-read after every action. " +
		"When the user's task is done, do NOT close their tabs unless they say so — this is their real browser."
	return utils.InferTool("browser_bridge", desc, func(ctx context.Context, in *browserBridgeInput) (*browserBridgeOutput, error) {
		return dispatchBrowserBridge(ctx, svc, in)
	})
}

func dispatchBrowserBridge(ctx context.Context, svc *browserbridge.Service, in *browserBridgeInput) (*browserBridgeOutput, error) {
	switch in.Action {
	case "extension_status":
		st := svc.ExtensionStatus()
		return &browserBridgeOutput{OK: true, Status: &st}, nil

	case "list_sessions":
		return &browserBridgeOutput{OK: true, Sessions: svc.ListSessions()}, nil

	case "list_pages":
		if in.BrowserID == "" {
			return &browserBridgeOutput{OK: false, Message: "list_pages 需要 browser_id"}, nil
		}
		pages, err := svc.ListPages(in.BrowserID)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Pages: pages}, nil

	case "open_tab":
		if in.BrowserID == "" || in.URL == "" {
			return &browserBridgeOutput{OK: false, Message: "open_tab 需要 browser_id 和 url"}, nil
		}
		active := true
		if in.Active {
			active = in.Active
		}
		data, err := svc.OpenTab(ctx, in.BrowserID, in.URL, active)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "focus_page":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "focus_page 需要 browser_id 和 page_id"}, nil
		}
		data, err := svc.FocusPage(ctx, in.BrowserID, in.PageID)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "close_tab":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "close_tab 需要 browser_id 和 page_id"}, nil
		}
		data, err := svc.CloseTab(ctx, in.BrowserID, in.PageID)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "read_state":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "read_state 需要 browser_id 和 page_id"}, nil
		}
		data, err := svc.ReadState(ctx, in.BrowserID, in.PageID)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "click":
		if in.BrowserID == "" || in.PageID == "" || in.Index == 0 {
			return &browserBridgeOutput{OK: false, Message: "click 需要 browser_id / page_id / index"}, nil
		}
		data, err := svc.Click(ctx, in.BrowserID, in.PageID, in.Index, in.Variant)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data, Message: fmt.Sprintf("%s index %d", chooseVariant(in.Variant), in.Index)}, nil

	case "type":
		if in.BrowserID == "" || in.PageID == "" || in.Index == 0 || in.Text == "" {
			return &browserBridgeOutput{OK: false, Message: "type 需要 browser_id / page_id / index / text"}, nil
		}
		data, err := svc.TypeText(ctx, in.BrowserID, in.PageID, in.Index, in.Text)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "press":
		if in.BrowserID == "" || in.PageID == "" || in.Key == "" {
			return &browserBridgeOutput{OK: false, Message: "press 需要 browser_id / page_id / key"}, nil
		}
		data, err := svc.Press(ctx, in.BrowserID, in.PageID, in.Key, in.Index)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	default:
		return &browserBridgeOutput{OK: false, Message: "unknown action: " + in.Action}, nil
	}
}

func chooseVariant(v string) string {
	if v == "" {
		return "click"
	}
	return v
}
