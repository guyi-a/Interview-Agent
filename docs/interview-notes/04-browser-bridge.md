# 浏览器操作 · browser_bridge（Chrome extension 接管用户 Chrome）

> 上一篇讲了 browser_use 起独立 Chromium 的路径，这一篇讲另一条：**用 WebSocket 接管用户已经打开的 Chrome**，让 agent 借用户的登录态跑事情。两条路径 SKILL 都写好，agent 会先探 bridge 有没有连上，再决定走哪条。

## 面试一句话答法

我做了一个 **Chrome extension + WebSocket** 的桥：extension 装在用户 Chrome 里，跟 backend 建 WS 长连接；agent 想操作浏览器时把命令（`browser_open_tab` / `browser_click` / ...）通过 WS 发给 extension，extension 用 chrome API 在**用户自己的 Chrome** 里执行，把结果回传。核心价值是**保留用户登录态**（Boss 直聘 / 内网 / 需要登录的 SaaS 都能操作），代价是需要用户装扩展。

发现协议靠一个签名过的 ping endpoint（extension 探本地能连哪个端口）+ 随机 token 做 WS auth。命令走**请求 - 响应模式**：每个 cmd 一个 uuid，backend 塞一个 pending channel 阻塞等，extension 回 `{type: "result", id}` 时按 uuid 唤醒对应 channel。

---

## 为什么要有 bridge

前提是 browser_use 已经能起独立 Chromium 操作页面了 —— 那为什么还要 bridge？

一句话：**独立 Chromium 没登录态**。

具体场景：
- Boss 直聘搜岗位：需要用户 Chrome 里登过 Boss，否则搜索结果稀少无薪资
- 公司内网系统：SSO 走用户 Chrome 里的 cookie
- 用户自己浏览到一半的页面，想让 agent 接着操作（"帮我把这个购物车结账"）

这些场景独立 Chromium 冷启动是**全新会话**，什么都要重新登。Bridge 直接让 agent 操作**用户自己的 Chrome**，登录态天然有。

代价：用户要装扩展。所以 SKILL 里把 bridge 定位为**首选路径**，browser_use 是**没扩展时的兜底**。

---

## 系统构成

```
┌──────────────────┐     ┌────────────────────┐
│  用户 Chrome     │     │  Interview-Agent   │
│  ├─ 网页        │     │  backend           │
│  ├─ Chrome API  │◀───▶│                    │
│  └─ Kro Bridge  │ WS  │  browserbridge/    │
│     Extension   │     │    Service         │
└──────────────────┘     │    Registry        │
                         └────────────────────┘
                                  ▲
                                  │
                                  ▼
                          ┌────────────────┐
                          │  agent tool    │
                          │  browser_bridge│
                          └────────────────┘
```

**后端三个组件**（`internal/agent/browserbridge/`）：

| 组件 | 职责 |
|---|---|
| **Registry** | 内存维护"当前有哪些 extension 连着 / 每个 extension 打开了哪些 tab" |
| **Service** | 命令请求 - 响应的核心，管 pending channel 表 + WS 写 |
| **WebSocket handler**（ws.go） | HTTP 升级 WS + 收帧分派 + 断连清理 |
| **actions.go** | 把 agent 语义的动作（open_tab / click / ...）翻译成 extension 认识的 `browser_*` 命令 |

---

## Discovery：extension 怎么找到 backend

Extension 装在 Chrome 里跑，backend 跑在用户本机某个端口（默认 9001）。Extension 需要**自己探测**能连哪个端口。

流程（`protocol.go` 定义常量）：

1. Extension 遍历常用端口列表（9001 / 9002 / ...）
2. 对每个端口 `GET /chrome-bridge/ping`，带一个签名 header
3. 后端签名匹配 → 返回一个 JSON payload（server 名、instance_id、协议版本、WS 路径、**discovery token**）
4. 用返回的 token 打 WS：`ws://localhost:9001/chrome-bridge/ws?token=<...>`

签名的算法：

```go
PingSignature = sha256("kro-browser-bridge:kro-2026")
```

Extension 请求头带 `X-Kro-Client-Sig: <sha256>`，backend `handlePing` 校验：

```go
if c.GetHeader(PingSigHeader) != PingSignature {
    c.JSON(403, ...)
    return
}
```

**签名有意义吗**：其实 signature 是一个**恒定值**（extension 和 backend 都硬编码），实际防不了刻意攻击（谁反编译 extension 都知道），只防**误连别的 localhost 服务**——比如用户本机跑了别的 WebSocket 服务也监听 9001，随便一 curl 就返回一坨 JSON 会把 extension 弄糊涂。签名相当于握手咒语，"你说得出这个咒语才可能是同族的"。

**Discovery token** 才是**真身份**：进程启动时 `uuid.NewString()`，每次重启换一个。Extension 拿到 token 后**只在本次 WS 建立时用一次**。这个 token 不落盘，重启 backend 就废，好处：token 泄漏（假设 extension 有 bug 被读走）也只影响这次进程生命周期。

---

## 连接生命周期

Extension 建 WS 后：

```
extension → GET /chrome-bridge/ws?token=<X>
backend   ↓  握手 upgrade
          ↓  校验 token（不对就 close code 4401 "auth failed"）
          ↓  RegisterClient()：分配 sessionID + 占位 browserID + writeLock
          ↓  attachConn(sessionID, conn)：进 connections map
extension → hello frame：{browser_id, browser_label, extension_version}
backend   ↓  SetBrowserID：用真 browser_id 替换占位
          ↓  写 hello_ack 回去
extension → page_updated frame (每次用户开/切 tab 自动推)
backend   ↓  Registry.UpsertPage()
...
extension → (WS 关)
backend   ↓  defer 里：detachConn + UnregisterClient
          ↓  detachConn 把所有 pending 命令全 fail 掉
```

两个 ID 的作用不同：
- **sessionID** 是 backend 侧的连接 ID（用来找 WS conn + writeLock）
- **browserID** 是 extension 自己维护的（同一个 Chrome profile 稳定，跨扩展重启不变）—— agent 从 `list_sessions` 拿到 browserID 做后续调用

先分配占位 browserID 是因为 WS 已经 upgrade 了但 hello 还没到，Registry 得先能索引这个 conn。Hello 到了再用真 browserID 覆盖。

---

## 消息协议（自己造的一套小协议）

所有 WS frame 都是 JSON object，带 `type` 字段。核心 6 种：

| type | 方向 | 用途 |
|---|---|---|
| `hello` | ext → be | 上报 browser_id / label / extension version |
| `hello_ack` | be → ext | 确认 hello 收到 |
| `command` | be → ext | 让 extension 执行动作（open_tab / click / ...） |
| `result` | ext → be | 命令成功回复，`id` 对应 command 的 id |
| `error` | ext → be | 命令失败回复 |
| `event` | ext → be | 主动事件（`payload.name` = `page_updated` / `page_closed`） |
| `ping` | ext → be | 心跳，忽略 |

`command` envelope 结构：

```go
type CommandEnvelope struct {
    Type      string         // "command"
    ID        string         // uuid，追踪响应
    SessionID string
    Timestamp int64
    Payload   CommandPayload
}
type CommandPayload struct {
    Tool      string                 // "browser_open_tab" 等
    Arguments map[string]interface{}
}
```

**为什么用 map[string]interface{} 装 args**：不同命令 args 结构差别大（open_tab 要 url，click 要 index+variant），Go 里做强类型意味着每加一个命令加一个 struct 类型，麻烦。JSON envelope 里 args 是自由格式，extension 那边 JS 天然弱类型，没差别。

**为什么不用 gRPC / MCP**：
- gRPC 要 proto 编译 + 双向 stream 客户端，extension 里跑 Node gRPC 客户端不现实
- MCP 是新协议，chrome extension 里的 stdio 语义不适用

自己造一个"WebSocket + JSON envelope + uuid 追踪"的小协议，最省事，也最贴 chrome extension 的能力（chrome extension 里 WebSocket 是原生 API）。

---

## 命令请求 - 响应的核心：pending channel 表

这是 bridge 最需要讲清楚的部分。看 `Service.SendCommand`：

```go
func (s *Service) SendCommand(ctx, browserID, tool, args) (map, error) {
    cmdID := uuid.NewString()
    ch := make(chan pendingResult, 1)

    s.mu.Lock()
    s.pending[cmdID] = ch     // ① 塞 pending 表
    s.mu.Unlock()

    err := s.writeJSON(sessionID, envelope{ID: cmdID, ...})  // ② 发 WS

    select {
    case res := <-ch:              // ③ 阻塞等回复
        return res.payload, res.err
    case <-ctx.Done():             // ④ 上游取消
        s.mu.Lock(); delete(s.pending, cmdID); s.mu.Unlock()
        return nil, ctx.Err()
    case <-time.After(30 * time.Second):   // ⑤ 30s 超时
        s.mu.Lock(); delete(s.pending, cmdID); s.mu.Unlock()
        return nil, fmt.Errorf("timed out")
    }
}
```

**关键**：backend 和 extension 是**独立进程**，通信全靠 WS。SendCommand 需要"同步返回给 caller"，但底层是异步的 —— 靠 **`map[cmdID] chan`** 桥接：

- 发命令时创建 channel 塞进 map
- WS 接收 goroutine 收到 `result` frame 时 `deliverResult(cmdID, payload)` 把 payload 塞进对应 channel
- SendCommand 从 channel 读到 → 返回

这个模式非常经典（RPC over WS 都长这样）。三个需要小心的点：

1. **channel 缓冲要 1**：`make(chan pendingResult, 1)` —— receive loop 发 result 时 SendCommand 可能已经因为 ctx / timeout 走了，如果 channel 无缓冲会阻塞 receive loop 一直等（然后整个 receive goroutine 就废了）。缓冲 1 让 send 非阻塞。

2. **timeout 分支要清 pending 表**：不然过期的 cmdID 会一直挂在 map 里 —— extension 后知后觉回一个 result，`deliverResult` 找不到对应 channel（已经因为 delete 拿不到了），但 pending 表还在长。所以 timeout / ctx.Done 分支里必须 `delete(s.pending, cmdID)`。

3. **断连要把所有 pending fail 掉**：`detachConn` 里：

```go
pending := s.pending
s.pending = make(map[string]chan pendingResult)   // 交换
s.mu.Unlock()

for id, ch := range pending {
    ch <- pendingResult{err: fmt.Errorf("browser websocket disconnected mid-command %s", id)}
    close(ch)
}
```

不然 extension 崩了以后所有 in-flight command 会等到 30s 超时才返回，agent 干等 30s。主动 fail 让 caller 立刻感知。

---

## Registry：session + browser + page 三级索引

Registry 是 backend 内存里对 extension 侧状态的**镜像**。三个 map：

```go
clients          map[sessionID] *BridgeClient  // WS 连接维度
clientsByBrowser map[browserID] *BridgeClient  // extension 维度（供 agent 用）
pages            map[pageID]    *BrowserPage   // 打开的 tab
```

`page` 结构关键字段：

```go
type BrowserPage struct {
    PageID       string  // backend 生成，稳定
    BrowserID    string  // 归属哪个 extension
    WindowID     int     // Chrome window
    TabID        int     // Chrome tab
    URL          string
    Title        string
    Active       bool    // 是否当前 tab
    ContextRole  string  // 用户上下文角色
    LastSeenAt   int64
}
```

**pageID 是 backend 生成的稳定 ID**（`"page_" + uuid[:8]`），extension 那边只知道 chrome 原生的 `tabID`。UpsertPage 用 `(browserID, tabID)` 做去重 key —— 同一个 tab 多次 page_updated 复用同一个 pageID。

**为什么要有 pageID**：
- Agent 拿到的 pageID 稳定，跨多次 read_state 不变，不用重新映射
- 隔离：pageID 只在 backend 有，extension 只用 tabID，如果 agent 手滑传了别的 browserID 的 pageID 也 lookup 不到（`GetPage` 里检查 `p.BrowserID == bid`）

---

## Page 状态是 extension 主动推的

Extension 装完后是"传感器"角色：只要 Chrome 里有 tab 变化（新开 / URL 变 / 关掉 / 切到前台），extension 就推一个 `event` frame 过来。backend 收到就 UpsertPage / RemovePage。

**好处**：agent 调 `list_pages` 时不用真的问 extension，直接从 Registry 内存里读。同时也不会漏掉状态（页面加载完了、URL 换了、tab 关了 —— extension 会主动推）。

**坏处**：Registry 里的状态**有延迟**。理论上 extension push 到 backend 也要几十 ms。Agent 一开完 `open_tab` 立刻 `read_state`，page 状态可能还没 push 过来（虽然 read_state 走 command → extension 里能直接读 chrome API，不依赖 Registry）。

---

## Actions：跟 browser_use 一一对应，但走 WS

Bridge 的 Actions 跟 use 完全对称：`OpenTab` / `ListPages` / `ClosePage` / `ReadState` / `Click` / `Type` / `Press` / `Scroll` / `WaitFor` / `Extract` / `ExecuteScript` / `Screenshot` / `GoBack` / `Reload` / ...

**实现方式**：全走 `SendCommand(ctx, browserID, "browser_XXX", args)` —— 把 tool 名和 args 塞进 command envelope，extension 那边接收后调用对应 chrome API 实现。

**agent 层看不出差别**：browser_use 和 browser_bridge 两个 tool 的 action 参数几乎一致（都是 `open_tab / read_state / click / ...` 那套），SKILL 里让 agent 优先选 bridge，不 ready 时切 use。这个"用户 API 兼容"的对称设计让 agent 上层不用关心具体走哪条路。

---

## action 之外：三个 bridge 独有的 meta action

`extension_status`：探测 extension 装了没、当前有几个 browser 连着、版本号是啥。**agent 首次调 bridge 必先跑这个** —— ready=false 就立刻停下告诉用户"没装扩展"。

`list_sessions`：列出当前所有连着的 browserID。多 Chrome 用户可能有一个 profile 一个 extension → 一个 session。

`list_pages(browserID)`：列出这个 browser 下所有 tab（从 Registry 读，不走 command）。

这三个都是**读 Registry 本地状态**，不真发命令 → 快 + 不受 WS 延迟影响。

---

## 断连处理

Extension 挂 / 用户关 Chrome / 网络断 → WS 收到 close：

```go
defer func() {
    svc.detachConn(client.SessionID)    // ① fail 所有 pending 命令
    svc.Registry.UnregisterClient(client.SessionID)  // ② 清 Registry
    _ = conn.Close()
    log.Printf("...")
}()
```

`UnregisterClient` 里除了清 client 索引，还会 **prune 该 browser 名下所有 page**：

```go
for pid, p := range r.pages {
    if p.BrowserID == c.BrowserID {
        delete(r.pages, pid)
    }
}
```

不然 extension 重连拿新 browserID 后，老 pageID 会作为"孤儿"留在 pages 里，下次 `list_pages` 返回旧数据混乱。

**重连**：Extension 会自动重连，重新走 discovery → 拿新 token → 建 WS → 发 hello。**新的 sessionID + 可能相同的 browserID**（extension 侧持久化）。老的 pending command 已经全 fail，agent 那边拿到 error 会知道要重跑。

---

## HTTP endpoints

Bridge 挂 2 个路径，跟其他业务 API 分开（`/chrome-bridge/` 前缀是硬编码到 extension 的）：

- `GET /chrome-bridge/ping` —— discovery，返回 token
- `HEAD /chrome-bridge/ping` —— liveness probe，不校验签名
- `GET /chrome-bridge/ws` —— WebSocket upgrade

不用 `/api` 前缀是因为 extension 里的 URL 是硬编码，改了要 extension 也发新版。这个前缀就是接口 versioning 的一部分。

---

## Agent tool 层：一个 mega-tool 分派

跟 browser_use 一样，browser_bridge 是一个 mega-tool 带 `action` 参数分派。差别：

- **browser_use** 是 conversation-scoped（session 挂 convID）
- **browser_bridge** 是 browser_id-scoped（agent 需要显式带 browser_id）

这是 bridge 的**多设备属性**导致的 —— 一个用户可能同时开 Mac Chrome + Windows Chrome 两个 profile 都装了 extension，agent 得选一个操作。SKILL.md 里教 agent 一律先 `list_sessions` 拿 browser_id 再用。

---

## 一次典型的 bridge 调用时序

用户："去 Boss 直聘搜一下'Go 后端'岗位"（Chrome 里已经登着 Boss）

```
Agent 决策：走 bridge（先探 extension_status）

browser_bridge(action='extension_status')
  → Registry.ListSessions()
  ← {ready: true, browser_ids: ["chrome_abc123"]}

browser_bridge(action='list_sessions')
  ← [{browser_id: "chrome_abc123", ...}]

browser_bridge(action='list_pages', browser_id='chrome_abc123')
  ← [{page_id: "page_xxx", url: "chrome://newtab", ...}]

browser_bridge(action='open_tab', browser_id='chrome_abc123',
              url='https://www.zhipin.com/web/geek/job?query=Go后端&city=101010100')
  → SendCommand("browser_open_tab", {url, browser_id, active})
     ↓  cmdID=uuid; pending[cmdID] = ch; write JSON
  ← ext 侧: chrome.tabs.create({url})
     → 用户 Chrome 里真的开一个新 tab（带用户登录态）
     → chrome.webNavigation onCompleted 后推 event
  ← ext → be: {type: "event", payload: {name: "page_updated", ...}}
  ← ext → be: {type: "result", id: cmdID, payload: {...}}
  → Service.deliverResult(cmdID, ...) → ch <-  唤醒 SendCommand
  ← {page: {...}, url: ...}
  
browser_bridge(action='wait_for', page_id='page_xxx', timeout_ms=8000)
  → SendCommand("browser_wait_for", {timeout_ms})
  ← ok

browser_bridge(action='read_state', page_id='page_xxx')
  → SendCommand("browser_read_state", ...)
  ← ext 侧: 跑 snapshotJS 一样的东西读 DOM
  ← state markdown + elements

browser_bridge(action='execute_script', page_id='page_xxx',
              script='(function(){...绕字体反爬...})()')
  → SendCommand("browser_execute_script", {script})
  ← ext 侧: chrome.scripting.executeScript(...)
  ← 抽出的岗位数据

Agent 拿数据整理返回给用户
（不主动 close_tab，用户 Chrome 保留搜索页给用户自己看）
```

---

## 跟 browser_use 的核心对比

| 维度 | browser_use | browser_bridge |
|---|---|---|
| 浏览器进程 | 独立 Chromium（Playwright 起） | 用户已开的 Chrome |
| 用户登录态 | 无 | **有** |
| 前置条件 | 装 Playwright driver + Chromium（~150MB） | 用户装 Chrome extension |
| 通信方式 | 进程内 Go 调用 Playwright API | WebSocket JSON |
| Session 隔离 | BrowserContext | Chrome profile |
| screenshot 强制 workspace | 是 | 是 |
| 破坏性风险 | 低（沙盒） | 高（在用户真 Chrome 上，可能改用户账号状态） |
| SKILL 优先级 | 兜底 | 首选 |

---

## 面试可能追问

**Q：为什么不让 backend 直接调 chrome DevTools Protocol（CDP）而要走 extension？**

A：CDP 需要 Chrome **启动时**加 `--remote-debugging-port` flag。用户已开的 Chrome 通常没加这个 flag，backend 想 attach 上去只有两条路：
1. **杀掉用户 Chrome 重启带 flag**：用户接受不了（打开的 tab / 窗口全没了）
2. **写 Chrome extension**：extension 装完就常驻，用户开 Chrome 就自动跑 —— 无侵入

选 extension 是唯一"用户 Chrome 保留原样"的方案。

**Q：Chrome extension 里 WebSocket 和 chrome.debugger API 有啥区别？**

A：
- **`chrome.debugger` API**：extension 能拿到 CDP 权限，直接跑 CDP 命令。**权限声明必须要"debugger"**，Chrome 会在顶部横幅显示"某扩展正在调试此浏览器" —— 用户看着不舒服，而且性能开销大
- **`chrome.tabs` / `chrome.scripting` / `webNavigation` API**：正常 extension 权限，能开 tab / 注入 JS / 监听导航事件，没有 debugger 横幅。**够用了**

我们走后者，用普通 API + webSocket 跟 backend 通信。

**Q：pending channel 表的锁粒度？**

A：整个 map 一把大锁。理论上可以拆到 per-cmdID 锁，但没意义：
1. Command 触发频率不高（一次浏览器操作也就几个 ms 内的 map 读写）
2. **不加锁的话** deliverResult 和 SendCommand 的 timeout 分支会 race —— 一个从 map 里 delete、另一个 send 到已 close 的 channel → panic

大锁 + `chan cap=1` 是最省心的组合。

**Q：Discovery token 泄漏怎么办？**

A：token 是**per-process** 的 —— backend 重启就换新的，老 token 直接失效。想利用泄漏 token 的攻击者必须：
1. 在 token 有效期内（backend 没重启）
2. 能连本地 WebSocket（用户机器）
3. 知道端口号

三个条件叠起来窗口很窄。真要严防会加"origin 校验"—— 但 chrome-extension:// origin 每个装 extension 的机器都不同，校验反而挡不住合法用户。当前接受这个风险。

**Q：extension 端 hello 之前 backend 已经 SetSessionID 了，如果 hello 一直不到怎么办？**

A：`SetBrowserID` 里给了占位 uuid。**如果 hello 一直不到**：
- Registry 里这个 client 用占位 browserID 挂着
- `ListSessions()` 会返回它，但 agent 拿这个占位 browserID 调命令时，extension 那边不认（extension 认自己的 browserID）→ 命令 timeout / error

**实际很难发生**：extension WS 一连上马上就发 hello。真出问题一般是 extension 版本太旧不发 hello，我们靠 30s command timeout 兜底。

**Q：为什么每个 sessionID 一个 writeLock，而不是全局一把锁？**

A：**gorilla/websocket 的 WriteJSON 不是并发安全的** —— 同一个 conn 两个 goroutine 一起写会 corrupt frame。所以每个 conn 一把锁。用 sessionID 索引锁 map 保证每个 conn 独立，多 conn 并发写不同 client 互不阻塞。

**Q：如果一个 browserID 下同时发多个 command 会怎么样？**

A：能同时发（`writeLocks` 保护的是 write frame 原子性，不阻塞并行 send）。**每个 command 独立 uuid + 独立 pending channel**，回来时按 uuid 唤醒对应 channel，互不干扰。

Extension 那边通常是**串行处理**（Chrome API 大多是 async but sequential），但 backend 层完全支持并发命令。

**Q：Bridge 的信任模型是什么？**

A：三层信任：
1. **backend 信任 extension**（token 校验）—— extension 拿到 token 就是"可信客户端"
2. **extension 信任 chrome**（chrome API）—— extension 用 chrome 官方 API，权限由 manifest 声明
3. **agent 信任用户**（approval）—— destructive 操作走审批（写文件、执行命令）

Bridge 里的 `execute_script` 权力很大（能在 Chrome 里跑任意 JS），当前**不走 approval**（跟 browser_use 一样，SKILL 里靠 agent 自律 + 明说）。生产上要收紧就把 bridge 的 execute_script 列入 approval 白名单。

**Q：Bridge 断了 agent 是不是就死了？**

A：断连时 `detachConn` 会把 pending 全 fail，所有 in-flight command 立刻拿到 error。SKILL 里教 agent 遇到这种 error 就退到 browser_use 或者告诉用户"扩展断了"，不会死循环。

**Q：Chrome extension 是外部的还是这个 repo 里的？**

A：目前 extension 是**独立仓库（Kro Browser Bridge）**，不在这个 repo 里。SKILL.md 里 "Kro Browser Bridge 扩展" 说的就是它。本 repo 只是**协议兼容**这个 extension（`kro-browser-bridge` clientID + salt `kro-2026` 就是跟 extension 对齐的）。这个协议名字继承自那个项目，短期不会改（改了就 break 已装的 extension）。

---

## 一句话小结

**Extension 常驻用户 Chrome、WS 长连到 backend、uuid 追踪命令请求 - 响应**。核心价值就是"我不启浏览器，我借你的浏览器"—— 拿到用户登录态，也承担对应的信任成本。跟 browser_use 是**互补关系**，同一个 API shape，SKILL 决定优先走哪条。
