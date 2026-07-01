package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
)

// browser_use tool: one mega-tool with an `action` verb. Kept a single tool
// so the UI's tool chips don't get 10 separate cards per turn.
//
// action 分支：
//   open_tab(url)            → 新 tab 加载 url
//   list_pages               → 当前 session 打开的所有 tab
//   close_tab(page_id)       → 关闭指定 tab
//   read_state(page_id)      → 返回 markdown + 元素编号（拿 index 做后续操作用）
//   click(page_id, index)    → 点击 read_state 里的第 N 个元素
//   type(page_id, index, text) → 往输入框填 text（先清空）
//   press(page_id, key, index?) → 键盘事件；不传 index 时作用于整页

type browserUseInput struct {
	Action string `json:"action" jsonschema:"description=One of: open_tab / list_pages / close_tab / read_state / click / type / press / close_session"`
	PageID string `json:"page_id,omitempty" jsonschema:"description=Page identifier returned by open_tab. Required for close_tab / read_state / click / type / press."`
	URL    string `json:"url,omitempty" jsonschema:"description=Absolute URL for open_tab, must include scheme (http/https/file)."`
	Index  int    `json:"index,omitempty" jsonschema:"description=Element index from the last read_state on this page. Required for click / type. Optional for press."`
	Text   string `json:"text,omitempty" jsonschema:"description=For type: what to fill into the element."`
	Key    string `json:"key,omitempty" jsonschema:"description=For press: key name like 'Enter', 'Tab', 'Escape', or a combo like 'Control+A'."`
}

type browserUseOutput struct {
	OK      bool             `json:"ok"`
	Message string           `json:"message,omitempty"`
	Page    *browseruse.PageInfo `json:"page,omitempty"`
	Pages   []browseruse.PageInfo `json:"pages,omitempty"`
	State   string           `json:"state,omitempty"`
}

func newBrowserUseTool(mgr *browseruse.Manager) (tool.BaseTool, error) {
	if mgr == nil {
		return nil, errors.New("browser_use: manager is nil")
	}
	desc := "Real browser automation via an independent Chromium instance. " +
		"First call must be open_tab to load a URL; use read_state to see the " +
		"interactive elements (indexed), then click / type / press by index. " +
		"Read_state must be called again after any action that changes the DOM " +
		"— element indices are only valid for the most recent read_state on that page. " +
		"When the task is done, call action='close_session' to release the browser. " +
		"If the tool complains that chromium isn't installed, first call " +
		"browser_use_install(step='check')."
	return utils.InferTool("browser_use", desc, func(ctx context.Context, in *browserUseInput) (*browserUseOutput, error) {
		return dispatchBrowserUse(ctx, mgr, in)
	})
}

func dispatchBrowserUse(ctx context.Context, mgr *browseruse.Manager, in *browserUseInput) (*browserUseOutput, error) {
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return nil, errors.New("browser_use: no conversation in context")
	}

	// close_session is the one action that must NOT lazily boot a browser
	// just to tear it down. Handle it before any Session() call.
	if in.Action == "close_session" {
		mgr.CloseSession(convID)
		return &browserUseOutput{OK: true, Message: "browser session closed"}, nil
	}

	sess, err := mgr.Session(ctx, convID)
	if err != nil {
		if errors.Is(err, browseruse.ErrDriverMissing) {
			return &browserUseOutput{
				OK:      false,
				Message: "浏览器依赖尚未就绪。先调 browser_use_install(step='check') 检查，再按提示 install。",
			}, nil
		}
		return nil, err
	}

	switch in.Action {
	case "open_tab":
		if in.URL == "" {
			return &browserUseOutput{OK: false, Message: "open_tab 需要 url"}, nil
		}
		info, err := sess.OpenTab(in.URL)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Page: &info}, nil

	case "list_pages":
		return &browserUseOutput{OK: true, Pages: sess.ListPages()}, nil

	case "close_tab":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "close_tab 需要 page_id"}, nil
		}
		if err := sess.CloseTab(in.PageID); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "closed"}, nil

	case "read_state":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "read_state 需要 page_id"}, nil
		}
		md, err := sess.ReadState(in.PageID)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, State: md}, nil

	case "click":
		if in.PageID == "" || in.Index == 0 {
			return &browserUseOutput{OK: false, Message: "click 需要 page_id 和 index"}, nil
		}
		if err := sess.Click(in.PageID, in.Index); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: fmt.Sprintf("clicked index %d", in.Index)}, nil

	case "type":
		if in.PageID == "" || in.Index == 0 || in.Text == "" {
			return &browserUseOutput{OK: false, Message: "type 需要 page_id, index, text"}, nil
		}
		if err := sess.Type(in.PageID, in.Index, in.Text); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: fmt.Sprintf("typed into index %d", in.Index)}, nil

	case "press":
		if in.PageID == "" || in.Key == "" {
			return &browserUseOutput{OK: false, Message: "press 需要 page_id 和 key"}, nil
		}
		if err := sess.Press(in.PageID, in.Key, in.Index); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "pressed " + in.Key}, nil

	default:
		return &browserUseOutput{OK: false, Message: "unknown action: " + in.Action}, nil
	}
}

// --- browser_use_install ---

type installInput struct {
	Step string `json:"step" jsonschema:"description=One of: check (probe local state) / install (download driver + chromium; ~150MB, slow)"`
}

type installOutput struct {
	Step          string `json:"step"`
	DriverReady   bool   `json:"driver_ready"`
	BrowsersReady bool   `json:"browsers_ready"`
	Message       string `json:"message"`
}

func newBrowserUseInstallTool() (tool.BaseTool, error) {
	desc := "Probe / install the playwright driver + chromium browser used by browser_use. " +
		"Call step='check' first; if not ready, call step='install' (large one-time download). " +
		"Everything lands in the OS user cache — nothing goes into the current workspace."
	return utils.InferTool("browser_use_install", desc, func(ctx context.Context, in *installInput) (*installOutput, error) {
		switch in.Step {
		case "check":
			st := browseruse.CheckInstall()
			return &installOutput{
				Step:          "check",
				DriverReady:   st.DriverReady,
				BrowsersReady: st.BrowsersReady,
				Message:       st.Message,
			}, nil
		case "install":
			if err := browseruse.DoInstall(); err != nil {
				return &installOutput{Step: "install", Message: "install 失败：" + err.Error()}, nil
			}
			st := browseruse.CheckInstall()
			return &installOutput{
				Step:          "install",
				DriverReady:   st.DriverReady,
				BrowsersReady: st.BrowsersReady,
				Message:       st.Message,
			}, nil
		default:
			return &installOutput{Step: in.Step, Message: "step 必须是 check 或 install"}, nil
		}
	})
}

