package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/browserbridge"
)

// maxBridgeResultBytes caps how big a single browser_bridge tool result
// can be after JSON serialisation. Some actions (execute_script, extract,
// read_state) can dump a whole page's DOM or a heavy JS return value into
// the tool result, and every past tool_result is replayed verbatim on the
// next ReAct iteration — so an uncapped 100 KiB result across 20 loop
// steps easily crosses the model's 1 M token ceiling. 32 KiB per call
// keeps ~30 iterations comfortably inside 1 M with room for prompt +
// assistant messages.
const maxBridgeResultBytes = 32 * 1024

type browserBridgeInput struct {
	Action      string `json:"action" jsonschema:"description=One of: extension_status / list_sessions / list_pages / open_tab / focus_page / close_tab / read_state / click / hover / dblclick / rightclick / type / press / scroll / wait_for / go_back / reload / extract / describe_element / execute_script"`
	BrowserID   string `json:"browser_id,omitempty" jsonschema:"description=Extension-reported stable id from list_sessions. Required for everything except extension_status / list_sessions."`
	PageID      string `json:"page_id,omitempty" jsonschema:"description=Page identifier from list_pages / open_tab. Required for every action that operates on a specific tab."`
	URL         string `json:"url,omitempty" jsonschema:"description=Absolute URL for open_tab."`
	Active      bool   `json:"active,omitempty" jsonschema:"description=For open_tab: whether the new tab should be foregrounded. Defaults true."`
	Index       int    `json:"index,omitempty" jsonschema:"description=Element index from the last read_state. Required for click / hover / dblclick / rightclick / type / extract / describe_element; optional for scroll / press."`
	Text        string `json:"text,omitempty" jsonschema:"description=For type: what to fill into the element."`
	Key         string `json:"key,omitempty" jsonschema:"description=For press: 'Enter' / 'Tab' / 'Escape' etc."`
	X           int    `json:"x,omitempty" jsonschema:"description=For scroll: horizontal pixels to scroll by (positive=right)."`
	Y           int    `json:"y,omitempty" jsonschema:"description=For scroll: vertical pixels to scroll by (positive=down)."`
	TimeoutMS   int    `json:"timeout_ms,omitempty" jsonschema:"description=For wait_for: max wait in milliseconds. Defaults to 10000."`
	IncludeHTML bool   `json:"include_html,omitempty" jsonschema:"description=For extract: also return the element's outerHTML."`
	Script      string `json:"script,omitempty" jsonschema:"description=For execute_script: JS expression or async function body returning a JSON-serialisable value."`
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
		"Requires the extension to be installed and connected — always start with " +
		"action='extension_status'. If ready=false, fall back to browser_use. " +
		"Typical flow: extension_status → list_sessions (pick browser_id) → list_pages / open_tab → " +
		"read_state (get index) → click/hover/dblclick/rightclick/type/press by index → read_state again. " +
		"Additional actions: scroll (x/y), wait_for (timeout_ms), go_back / reload, extract " +
		"(index + include_html), describe_element (upgrade index to stable selector), execute_script (raw JS). " +
		"Element indices only stay valid until the DOM changes; re-read after every action. " +
		"Do NOT close the user's tabs unless they explicitly ask — this is their real browser."
	return utils.InferTool("browser_bridge", desc, func(ctx context.Context, in *browserBridgeInput) (*browserBridgeOutput, error) {
		out, err := dispatchBrowserBridge(ctx, svc, in)
		if err != nil || out == nil || out.Data == nil {
			return out, err
		}
		out.Data = capBridgeData(out.Data, in.Action)
		return out, nil
	})
}

// capBridgeData replaces an oversized action Data map with a truncated
// preview stub so the LLM's context doesn't blow up on a chatty script.
// The stub keeps enough of the original JSON prefix that a model can
// often still extract the top-N items it cares about, and includes a
// clear "truncated" marker so it knows to narrow its next query rather
// than assuming the tail is missing entries.
func capBridgeData(data map[string]interface{}, action string) map[string]interface{} {
	b, err := json.Marshal(data)
	if err != nil {
		// JSON must succeed for us to have a size; if it doesn't, just pass
		// the map through — a downstream serialization would have failed
		// anyway and produced a clearer error.
		return data
	}
	if len(b) <= maxBridgeResultBytes {
		return data
	}
	// Keep a bit of headroom so the wrapping stub itself stays under the
	// cap after the preview is appended.
	const stubOverhead = 512
	previewCap := maxBridgeResultBytes - stubOverhead
	if previewCap < 0 {
		previewCap = 0
	}
	preview := string(b[:previewCap])
	return map[string]interface{}{
		"truncated":           true,
		"action":              action,
		"original_size_bytes": len(b),
		"kept_size_bytes":     len(preview),
		"note": fmt.Sprintf(
			"result was %d bytes, exceeded %d byte cap; showing first %d bytes of the raw JSON. "+
				"The prefix may end mid-token. Narrow the script (paginate, select fewer fields, limit N) "+
				"instead of retrying the same call.",
			len(b), maxBridgeResultBytes, len(preview),
		),
		"preview_json": preview,
	}
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

	case "click", "hover", "dblclick", "rightclick":
		if in.BrowserID == "" || in.PageID == "" || in.Index == 0 {
			return &browserBridgeOutput{OK: false, Message: in.Action + " 需要 browser_id / page_id / index"}, nil
		}
		data, err := svc.Click(ctx, in.BrowserID, in.PageID, in.Index, in.Action)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data, Message: fmt.Sprintf("%s index %d", in.Action, in.Index)}, nil

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

	case "scroll":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "scroll 需要 browser_id / page_id"}, nil
		}
		data, err := svc.Scroll(ctx, in.BrowserID, in.PageID, in.X, in.Y, in.Index)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data, Message: fmt.Sprintf("scrolled by (%d,%d)", in.X, in.Y)}, nil

	case "wait_for":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "wait_for 需要 browser_id / page_id"}, nil
		}
		data, err := svc.WaitFor(ctx, in.BrowserID, in.PageID, in.TimeoutMS)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data, Message: "wait done"}, nil

	case "go_back":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "go_back 需要 browser_id / page_id"}, nil
		}
		data, err := svc.GoBack(ctx, in.BrowserID, in.PageID)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data, Message: "navigated back"}, nil

	case "reload":
		if in.BrowserID == "" || in.PageID == "" {
			return &browserBridgeOutput{OK: false, Message: "reload 需要 browser_id / page_id"}, nil
		}
		data, err := svc.Reload(ctx, in.BrowserID, in.PageID)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data, Message: "reloaded"}, nil

	case "extract":
		if in.BrowserID == "" || in.PageID == "" || in.Index == 0 {
			return &browserBridgeOutput{OK: false, Message: "extract 需要 browser_id / page_id / index"}, nil
		}
		data, err := svc.Extract(ctx, in.BrowserID, in.PageID, in.Index, in.IncludeHTML)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "describe_element":
		if in.BrowserID == "" || in.PageID == "" || in.Index == 0 {
			return &browserBridgeOutput{OK: false, Message: "describe_element 需要 browser_id / page_id / index"}, nil
		}
		data, err := svc.DescribeElement(ctx, in.BrowserID, in.PageID, in.Index)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	case "execute_script":
		if in.BrowserID == "" || in.PageID == "" || in.Script == "" {
			return &browserBridgeOutput{OK: false, Message: "execute_script 需要 browser_id / page_id / script"}, nil
		}
		data, err := svc.ExecuteScript(ctx, in.BrowserID, in.PageID, in.Script)
		if err != nil {
			return &browserBridgeOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserBridgeOutput{OK: true, Data: data}, nil

	default:
		return &browserBridgeOutput{OK: false, Message: "unknown action: " + in.Action}, nil
	}
}
