# 浏览器操作 · browser_use（独立 Chromium 路径）

> 项目有两条浏览器路径：**browser_use**（本篇，走 playwright-go 起一个独立 Chromium）和 **browser_bridge**（下一篇，接管用户 Chrome）。这一篇只讲 browser_use。

## 面试一句话答法

用 [playwright-go](https://github.com/playwright-community/playwright-go) 起一个独立 Chromium 进程，一个 conversation 一个 `BrowserContext`（隔离 cookie/storage），LLM 通过 **"read_state → click by index"** 模式操作页面：`read_state` 跑 DOM 遍历 JS 抓所有可交互元素返回**编号 markdown 给 LLM 看**，selector **留服务端 cache 不给 LLM**；LLM 按 index 发 `click(index=N)`，服务端从 snapshot 里查 selector 转 Playwright Locator 点击。所有截图强制落到 workspace scope 里，防止越权写盘。

---

## 为什么要独立 Chromium

用户场景分两类：

- **需要用户登录态**（Boss 直聘、公司内网）→ 独立 Chromium 登不上，必须走 **browser_bridge** 接管用户 Chrome
- **不需要登录态 / 需要隔离环境 / 需要跨用户可重放**（爬公开信息、通用页面测试）→ 独立 Chromium 更合适：不污染用户 Chrome、可以跑 headless、可以精确控制 UA / viewport

**skills/browser-use/SKILL.md 里的定位**（[frontmatter](../../internal/agent/skills/browser-use/SKILL.md#L2)）：**"兜底路径"** —— 只在 browser_bridge 的 extension_status.ready=false（用户没装扩展）时才用。有扩展就优先走 bridge。

---

## 三层架构

```
Manager  (singleton, main.go 建的)
  ├── Playwright   (进程级：跑 playwright driver)
  ├── Browser      (进程级：一个 Chromium 主进程)
  └── sessions map[convID] *Session
                       ├── BrowserContext  (隔离 cookie / storage / cache)
                       └── pages map[pageID] *pageEntry
                                                ├── playwright.Page
                                                └── snapshot []Element  ← read_state 缓存
```

**层次隔离**：
- Playwright driver 全局共享（起一次，跨所有 conversation）
- Browser 主进程全局共享
- 每个 conversation 独占一个 BrowserContext —— **相当于一个"隐私窗口"**，cookie / localStorage / cache 都独立
- 每个 tab 是一个 Page

### Session 生命周期 [manager.go:43](../../internal/agent/browseruse/manager.go#L43)

```go
func (m *Manager) Session(ctx context.Context, convID string) (*Session, error) {
    m.mu.Lock()
    defer m.mu.Unlock()

    if s, ok := m.sessions[convID]; ok {
        return s, nil                       // 幂等，同 conv 只建一个
    }
    if err := m.ensureBrowserLocked(ctx); err != nil {
        return nil, err                     // Playwright / Chromium 没装 → ErrDriverMissing
    }
    bctx, _ := m.browser.NewContext()       // 独立 context
    s := newSession(convID, bctx, func() { m.CloseSession(convID) })
    m.sessions[convID] = s
    return s, nil
}
```

**懒启动**：第一次 `browser_use` 才启 Playwright + 下载 Chromium 首次 boot。不用不启，用完不留。

**懒销毁**：最后一个 session 关掉时 Manager.CloseSession 会把 **Browser + Playwright 也一起停** [manager.go:80-101](../../internal/agent/browseruse/manager.go#L80)：

```go
shutdownBrowser := len(m.sessions) == 0
...
if shutdownBrowser {
    _ = browser.Close()      // Chromium 主进程退出
    _ = pw.Stop()            // playwright driver 退出
}
```

这个设计**避免了"用户一天开一次对话，Chromium 主进程占内存跑一整天"** —— 一次 close_session 之后系统资源真正释放。下次调 `browser_use` 会重新 lazy boot。

**关闭链**：
- **显式**：`browser_use(action='close_session')`
- **最后 tab 关**：`CloseTab` 里检测 `empty := len(s.pages) == 0`，触发 `onEmpty()` → `Manager.CloseSession(convID)`
- **服务器关**：`Manager.Shutdown()`（在 main.go 里 defer 调）

---

## 核心 primitive：read_state

这是整个 browser_use 的**心脏**。给 LLM 一份**编号可交互元素列表**，让它按 index 引用而不是让它自己写 selector。

### 为什么这样设计

**如果让 LLM 自己写 selector**，会踩两个坑：
1. LLM 不知道页面的真实 DOM 结构（除非把整页 HTML 塞进去 —— 一个中等页面 200 KB HTML 直接爆 context）
2. 写出来的 selector 可能对（`button.submit-btn`）但**页面上有多个**，click 命中错的

**用 index 模式**：
- 服务端遍历 DOM 一次，只留可交互元素（`a, button, input, textarea, select, [role="button"], [role="link"], [role="tab"], [role="menuitem"], [contenteditable="true"]`）
- 服务端为**每个元素**算一个稳定 selector（id > data-testid > name > xpath 兜底）
- 只把 index + attrs + text 给 LLM（每个元素约 50-100 char，一页 20-30 元素总共 3KB 左右）
- selector 留服务端 pageEntry.snapshot 里 cache

### snapshotJS [state.go:13](../../internal/agent/browseruse/state.go#L13)

```js
const SELECTORS = 'a, button, input, textarea, select, [role="button"], ...';
const nodes = Array.from(document.querySelectorAll(SELECTORS));
const isVisible = (el) => {
  const rect = el.getBoundingClientRect();
  if (rect.width < 2 || rect.height < 2) return false;
  const style = window.getComputedStyle(el);
  if (style.visibility === 'hidden' || style.display === 'none') return false;
  if (parseFloat(style.opacity) === 0) return false;
  return true;
};
// selector 优先级：#id > [data-testid] > input[name] > xpath 兜底
const selectorOf = (el) => {
  if (el.id && /^[a-zA-Z_][\w-]*$/.test(el.id)) return '#' + el.id;
  const tid = el.getAttribute('data-testid');
  if (tid) return '[data-testid="' + tid + '"]';
  const name = el.getAttribute('name');
  if (name && (el.tagName === 'INPUT' || 'TEXTAREA' || 'SELECT')) {
    return el.tagName.toLowerCase() + '[name="' + name + '"]';
  }
  return 'xpath=' + xpath(el);
};
return { title, url, nodes: [...] };
```

**selector 兜底策略讲究**：
1. **`#id`**：最稳，但要求 id 是合法 CSS ident（否则 CSS 引擎报错，还得转义）
2. **`[data-testid]`**：现代前端框架的钩子，比 class 稳
3. **`input[name=...]`**：表单元素，`name` 属性稳
4. **xpath**：全部失败时兜底 —— **不稳**（依赖同级顺序），但至少能命中

### 返回给 LLM 的 markdown [state.go:148](../../internal/agent/browseruse/state.go#L148)

```
# Google
URL: https://www.google.com

[1] <input> (type=text name=q placeholder="搜索")
[2] <button> (type=submit)  Google Search
[3] <button> (type=submit)  I'm Feeling Lucky
[4] <a> (href=/accounts)  登录
...
```

LLM 看到编号 + 属性 + text 就够决策了。

### Snapshot 的**时效性**

每个 pageEntry 缓存最新一次 read_state 的 elements：

```go
s.mu.Lock()
e.snapshot = elems
s.mu.Unlock()
```

后续 `click(index=N)` 走 [elementByIndex](../../internal/agent/browseruse/state.go#L131) 反查：

```go
if len(e.snapshot) == 0 {
    return nil, fmt.Errorf("index %d 无效：先调 read_state 生成新快照", index)
}
if index < 1 || index > len(e.snapshot) {
    return nil, fmt.Errorf("index %d 越界：当前快照只有 %d 项，先调 read_state 拿新 index", ...)
}
```

**核心约束**：**任何一次 DOM 变化都可能让 snapshot 失效**。SKILL.md 里明确要求 "每次交互后再 read_state 一次"。

**为什么不自动 re-snapshot**：因为不是每次 action 都改 DOM（`extract` / `screenshot` 都不改），无脑 re-snapshot 浪费 token；也很难判断 DOM 变化时机（网络请求、动画、setTimeout 都可能触发）。**约定让 LLM 主动 re-read**，简单可靠。

---

## Action 全表

一个 mega-tool `browser_use`，按 `action` 字段分派（[browser_use.go:32](../../internal/agent/tools/browser_use.go#L32)）：

| Action | 参数 | 服务端调用 |
|---|---|---|
| `open_tab` | url | `sess.OpenTab(url)` → `bctx.NewPage() + page.Goto(url)` |
| `list_pages` | — | 遍历 session.pages |
| `focus_page` | page_id | `page.BringToFront()` |
| `close_tab` | page_id | `page.Close()`（最后一个自动 close_session） |
| `close_session` | — | `mgr.CloseSession(convID)` |
| `read_state` | page_id | 跑 snapshotJS → 缓存 snapshot → 返回 markdown |
| `click` / `hover` / `dblclick` / `rightclick` | page_id, index | index→selector→Locator + Playwright API |
| `type` | page_id, index, text | `loc.Fill("") + loc.Fill(text)` |
| `press` | page_id, key, index? | 有 index 走 `loc.Press(key)`，没 index 走 `page.Keyboard().Press(key)` |
| `scroll` | page_id, dx, dy | `page.Evaluate("window.scrollBy(...)")` |
| `wait_for` | page_id, selector 或 timeout_ms | `WaitForSelector` 或 `WaitForTimeout` |
| `go_back` / `reload` | page_id | `page.GoBack()` / `page.Reload()` |
| `extract` | page_id, index, include_html? | `loc.TextContent() + loc.InnerHTML()` |
| `screenshot` | page_id, save_path?, full_page? | `page.Screenshot()` + workspace 落盘 |
| `execute_script` | page_id, script | `page.Evaluate(script)` |

**为什么一个 mega-tool**：这是**故意**的产品决策 —— UI 上一个 turn 可能有十几次浏览器操作，一 action 一 tool 会让工具卡片刷屏。合成一个 `browser_use` 卡片，args 里带 action，视觉更干净。

---

## screenshot 的 workspace-scoped 保护

浏览器截图是**从沙盒外挥手写文件**的动作，容易被 LLM 幻觉带偏（`save_path="/etc/foo.png"`）。所以走一层强校验 [browser_use.go:282](../../internal/agent/tools/browser_use.go#L282)：

```go
func resolveScreenshotPath(ctx, convRepo, projectRepo, savePath string) (string, error) {
    ws, err := resolveConversationWorkspace(ctx, convRepo, projectRepo)
    if err != nil {
        return "", fmt.Errorf("screenshot 需要工作区")  // ① 必须先建 workspace
    }
    if savePath == "" {
        return filepath.Join(ws, "screenshots/shot-<timestamp>.png"), nil  // ② 自动命名
    }
    if filepath.IsAbs(savePath) {
        return "", fmt.Errorf("save_path 不接受绝对路径")   // ③ 拒绝绝对路径
    }
    abs, err := scope.Resolve(ws, savePath)                                   // ④ 防 ".." 越权
    if err != nil {
        return "", fmt.Errorf("save_path 越界: %w", err)
    }
    return abs, nil
}
```

**四层保护**叠加：
1. 强制先有 workspace
2. 允许省 save_path，自动 workspace 内命名
3. 拒绝绝对路径
4. `scope.Resolve` 里检测 `..` 逃逸

---

## Playwright 依赖：check + install 分离

Playwright 依赖两个东西：
- **driver**（几 MB 的 native binary）
- **chromium browser**（150+MB）

用户首次运行时都没有。策略：**不在 Manager 启动时装，第一次调 browser_use 时装**，且**装的动作暴露成一个独立工具** `browser_use_install`。

[install.go:21](../../internal/agent/browseruse/install.go#L21)：

```go
// 探测：DryRun install，能跑就说明已装
func CheckInstall() InstallStatus {
    err := playwright.Install(&playwright.RunOptions{
        DryRun: true, Browsers: []string{"chromium"},
    })
    if err != nil { return {Message: "尚未安装..."} }
    return {DriverReady: true, BrowsersReady: true, Message: "就绪"}
}

// 真装
func DoInstall() error {
    return playwright.Install(&playwright.RunOptions{
        Browsers: []string{"chromium"}, Verbose: true,
    })
}
```

**为什么拆两个 step**：
1. `check` 快（DryRun 只探测）→ 每次任务开头调
2. `install` 慢（150MB 下载）→ **不透明的 spinner** 不友好，暴露成显式工具让 LLM 告诉用户"我要下 150MB 的东西，请稍等"

Manager.Session 遇到 `ErrDriverMissing` 会返回一个软错误消息，引导 LLM 去调 `browser_use_install`。

---

## Chromium 三种模式（Channel）

[manager.go:15](../../internal/agent/browseruse/manager.go#L15)：

```go
type Config struct {
    Headless bool
    SlowMoMS float64
    Channel  string  // "" / "chrome" / "msedge"
}
```

- **`Channel=""`**：Playwright 下载的独立 chromium（默认，150MB 首次装）
- **`Channel="chrome"`**：复用系统 Chrome install —— **省下 chromium 下载**，但仍需 driver
- **`Channel="msedge"`**：复用 Edge

**当前项目走 `PLAYWRIGHT_CHANNEL` 环境变量传**（main.go）：默认空（走 bundled chromium），用户想省磁盘就 `export PLAYWRIGHT_CHANNEL=chrome`。

---

## 逃生舱：execute_script

不管抽象层多好，总有场景需要**跑任意 JS**。例如：
- 读 Vue 组件的 `$data`（Boss 直聘反爬用这个绕字体）
- 读 `window.__NEXT_DATA__` / `window.__NUXT__`
- 触发 `element.dispatchEvent(new Event('input'))`
- 从 iframe 里读值

`execute_script` [actions.go:80](../../internal/agent/browseruse/actions.go#L80) 直接透传给 `page.Evaluate`，返回 JSON 可序列化的 value：

```go
func (s *Session) ExecuteScript(pageID, script string) (interface{}, error) {
    e, err := s.page(pageID)
    if err != nil { return nil, err }
    return e.page.Evaluate(script)
}
```

**约束**：脚本必须返回 JSON 可序列化。SKILL.md 里点了一句"没有隐式 return，请显式 `return ...`"，坑过 LLM 几次。

---

## 一次典型的 browser_use 时序

用户："帮我从 example.com 搜索'golang tutorials'"

```
Agent 决策 → load_skill(browser-use)
  ↓
browser_use_install(step='check')
  → driver_ready=true, browsers_ready=true
  ↓
browser_use(action='open_tab', url='https://www.example.com')
  → Manager.Session(convID)      ← 懒启动 Playwright + Chromium
  → bctx.NewPage() + page.Goto(url)
  ← {page_id: 'page_a1b2c3d4', url: '...', title: '...'}
  ↓
browser_use(action='read_state', page_id='page_a1b2c3d4')
  → 跑 snapshotJS → 编号 elements → snapshot cache
  ← "# Example\nURL:...\n[1] <input>...\n[2] <button>Search..."
  ↓
Agent 看到 [1] 是搜索框 → 
browser_use(action='type', page_id='page_a1b2c3d4', index=1, text='golang tutorials')
  → elementByIndex(1) → selector `input[name="q"]`
  → loc.Fill("") + loc.Fill("golang tutorials")
  ↓
browser_use(action='press', page_id='page_a1b2c3d4', key='Enter')
  → page.Keyboard().Press("Enter")   ← 没 index，作用于整页
  ↓
browser_use(action='wait_for', page_id='page_a1b2c3d4', selector='.results', timeout_ms=8000)
  → 等结果 DOM 出现
  ↓
browser_use(action='read_state', page_id='page_a1b2c3d4')
  → 重新 snapshot（DOM 变了）
  ← 新一批 elements（[1..N] 是搜索结果链接）
  ↓
Agent 决定挑第 3 个结果查看 → 
browser_use(action='click', page_id='page_a1b2c3d4', index=3)
  ↓
... 继续 read_state → 交互 → ...
  ↓
browser_use(action='screenshot', page_id='page_a1b2c3d4', save_path='result.png')
  → 落到 workspace/screenshots/result.png (auto-scoped)
  ↓
browser_use(action='close_session')
  → Session.close() → CloseSession(convID)
  → 最后一个 session → Browser.Close() + pw.Stop()
  → Chromium 主进程退出
```

---

## 面试可能追问的点

**Q：为什么选 Playwright 而不是 Puppeteer 或 Selenium？**

A：三个原因：
1. **多浏览器**：Playwright 一套 API 跨 Chromium / Firefox / WebKit，Puppeteer 只 Chrome
2. **auto-wait**：Playwright 的 Locator 自动等元素稳定后再点，Selenium 手动 `sleep` 或 `WebDriverWait`
3. **Go 生态**：`playwright-community/playwright-go` 是活跃的 Go 端口，Puppeteer 官方只有 Node

**Q：为什么每个 conversation 一个 BrowserContext 而不是一个 Browser？**

A：**共享 Chromium 主进程省内存**（一个 Chromium 主进程 ~200MB，每个额外 context 只加 ~20MB），同时 BrowserContext 提供了完整的 cookie/storage 隔离（跟"隐私窗口"等价）。

如果每个 conversation 一个 Browser：
- N 个对话 = N 个 Chromium 主进程 = 内存爆炸
- 启动时间：每个新对话都要冷启 Chromium（3-5 秒）

**Q：read_state → index 模式的缺点是什么？**

A：三个缺点：
1. **DOM 变化后 index 失效**：SPA 应用（React / Vue）里点一下 button 可能重排整个列表，index 全乱。解法：约定"每次交互后重新 read_state"（SKILL.md 里明确）
2. **只捕获"可交互"元素**：SELECTORS 是白名单（a/button/input/textarea/select/role=...），漏掉的元素得靠 `execute_script` 兜底
3. **selector 兜底 xpath 不稳**：id/data-testid/name 三层兜底都失败时走 xpath，页面结构改一下就断。生产上会有小比例的 flaky click

**Q：如果 LLM 反复 read_state / click 卡死怎么办？**

A：项目层面：`ChatService.Cancel(convID)` 触发 `buf.Cancel()`，agent runCtx 收到取消信号退出。tool 层没做单独 timeout —— 因为 Playwright 自身有 default timeout（30s）；如果模型自己想设 `timeout_ms`，`wait_for` 允许指定。

再往上：eino runner 有 `MaxIterations`（我们设 20-50），死循环最多跑 50 步就自动停。

**Q：browser_bridge 已经能接管用户 Chrome，为什么还要 browser_use？**

A：两个场景 bridge 不能替代：
1. **服务器 / headless 场景**：agent 跑在没 GUI 的 Linux 服务器上，用户 Chrome 根本不存在，只能用 headless Chromium
2. **需要环境隔离**：跑测试、爬公开数据、批量操作，用户 Chrome 有用户自己的登录 / 插件 / 历史，会互相污染

SKILL.md 把 browser_use 定位为"兜底路径" —— 有 bridge 时优先 bridge（能带用户登录态），没 bridge 才 use。

**Q：Chromium 主进程占内存怎么办？**

A：**用完就杀**。`Manager.CloseSession` 里检查 `shutdownBrowser := len(m.sessions) == 0`，最后一个 session 关时 `browser.Close() + pw.Stop()`。整个 Chromium 主进程会真的退出。下次调 browser_use 会再 lazy boot。

这跟"守护常驻"的做法反过来 —— 服务器上多个 conversation 都不用 browser_use 时，我们**不占内存**。

**Q：`execute_script` 会不会有安全问题？**

A：LLM 可以跑任意 JS，理论上能读 cookie / 提交表单 / 修改 DOM。当前接受这个风险，理由：
1. **独立 Chromium** 里的 cookie 只是 agent 自己在这一次 session 里 accumulate 的，用户没登录任何真实账号
2. **没 host bridge 通道** —— JS 跑在浏览器内部，能干的最多是网页级操作，不能访问文件系统 / 系统 API
3. `execute_script` 会走**审批**吗？—— 当前不走（policy.go 里没列）。属于**"信任 LLM 会跟用户明说这次要跑 JS"** 的默认安全模型。生产上要加就往 `NeedsApproval` 里加一条

**Q：怎么判断"上一步操作实际生效了"？**

A：SKILL.md 里明确要求"每次交互后 read_state 一次" —— 靠 URL / 元素变化推断。**tool 层不做副作用验证** —— action 返回 `OK: true` 只代表 Playwright 调用没报错，不代表业务动作成功（点了按钮但页面没反应也算"成功"）。这是 LLM 的判断责任。

---

## 一句话小结

**独立 Chromium + read_state 编号索引 + selector 服务端 cache + workspace-scoped 截图**：让 LLM 用最省 token 的方式操作真实浏览器，把状态复杂度全压到服务端。
