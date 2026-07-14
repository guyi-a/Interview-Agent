# krow 两条浏览器路径：use 的优化 + bridge 的合力实现

> **给你面试用**。文档目标：把 krow 的两条浏览器路径讲清楚，按你简历的两块叙事组织：**Part A：browser_use 的优化**（内部提升，让老路径达到生产可用）+ **Part B：browser_bridge 的合力实现**（作为下一代能力的进化）。两块**独立叙述**，不是"bridge 淘汰 use"的对比腔调。

## 一句话概括

**两条路径并存互补**：`browser_use` 通过工程质量迭代成为**稳定的独立浏览器路径**（面向不需要用户登录态的场景）；`browser_bridge` 是**新架构的登录态友好方案**（Chrome extension + WebSocket），跟 use 是同类工具的两条互补路线，不是替换关系。

面试怎么开场：
> "krow 的 browser 能力有两条路径。老的 browser_use 是基于开源 CLI 的独立浏览器路径，我们通过一系列工程优化让它从最初的 PoC 变成稳定的生产工具。同期我们合力开发了 browser_bridge —— 一套自研的 Chrome extension + WebSocket 桥，专门解决 use 无法覆盖的登录态场景。两条路径并存，agent 按场景选。"

---

# Part A：browser_use 的优化

`browser_use` 本质是**通过子进程跑开源 `browser-use` CLI**。原始 PoC 就是 spawn 一个进程执行命令，我们围绕它做了一系列工程化优化，让它达到生产可用。

**代码位置**：`app/internal/tools/browser.py`（约 1000 行）

## 8 个真实的优化方向

### 1. Workspace-scoped venv 安装（磁盘隔离）

**痛点**：早期方案是全局装 `browser-use`，多 workspace 用同一份 CLI —— 一个 workspace 升级依赖就影响所有；卸载 workspace 时 CLI 残留。

**优化**：每个 workspace 一份独立 venv（`.venv/bin/browser-use`），安装完全隔离。
- `_workspace_browser_use_bin(workspace_dir)` 返回 workspace-local binary 路径
- 跨平台适配：macOS/Linux 是 `.venv/bin/`，Windows 是 `.venv/Scripts/`

**代价**：磁盘占用增加（每个 workspace 300-500MB）—— 但换来的是隔离性和可清理性。**面试可讲的取舍**："我们选磁盘换隔离，因为 CLI 依赖冲突比磁盘紧张更难调"。

### 2. 分步安装 + 进度可视化（用户体验）

**痛点**：`uv add browser-use` + `playwright install chromium` 加起来 150-300MB 下载，一口气跑用户看着黑屏 3-5 分钟以为卡死。

**优化**：`install_browser_use` 工具支持 3 个 step（check / install / chromium），每步独立返回 `InstallResult{step, success, message, next_step}`，agent 拿到 `next_step` 引导用户"下一步该做啥"，前端能显示进度。

- **check**：探测 CLI + 浏览器是否已就绪 → 返回下一步
- **install**：`uv add browser-use` 装 CLI
- **chromium**：装 Playwright chromium（如果系统 Chrome 已装则跳过）

**关键设计**：每步都通过 `ctx.deps.emit_data_chunk("tool-progress", ...)` 推**实时进度事件**给前端，用户看着进度条不焦虑。

**面试话术**：
> "browser_use 首次安装要 150-300MB 下载，最初一口气跑用户体验很差。我拆成 check / install / chromium 三步 tool，每步返回 next_step 让 agent 引导用户，同时用 emit_data_chunk 推实时进度事件到前端。用户看得到'现在在下 Chromium'这种明确状态，减少焦虑。"

### 3. System Chrome 优先 + Chromium fallback（安装省时）

**痛点**：无脑装 Playwright Chromium 是 150MB+ 下载，但用户机器大概率已装 Google Chrome。

**优化**：`_is_system_chrome_installed()` 探测系统 Chrome 是否已装（macOS `/Applications/`、Windows `Program Files` + `%LOCALAPPDATA%`、Linux `/usr/bin/`），已装就跳过 Chromium 下载。
- browser-use CLI 本身也是"优先用系统 Chrome" 的策略，所以完美对齐
- `_is_browser_available()` = 系统 Chrome 或 Chromium 任一存在就算 OK

**收益**：多数 macOS/Windows 用户完全不用下 Chromium，安装时间从 3-5 分钟降到 30-60 秒。

### 4. 跨平台安装适配（Windows 是硬骨头）

**痛点**：`uv` / `browser-use` 在 macOS/Linux 好装，Windows 上 `uv` 需要 Git Bash 环境，`playwright` 装 Chromium 路径不同。

**优化**：`browser.py` 里所有路径判断都做 `platform.system()` 分支：
- Chrome 路径：Darwin / Windows / Linux 各一套
- venv binary 路径：Windows 是 `.venv/Scripts/*.exe`，其他是 `.venv/bin/*`
- Chromium 缓存：macOS `~/Library/Caches/ms-playwright`，Windows `%LOCALAPPDATA%/ms-playwright`，Linux `~/.cache/ms-playwright`

**面试话术**：
> "Windows 环境是硬骨头 —— uv 依赖 Git Bash、Playwright 缓存路径不同、venv binary 后缀不同。我把所有路径判断收敛到 platform.system() 分支，测试覆盖 macOS / Windows / Linux 三平台。"

### 5. Session 稳定化 + 跨调用状态复用（生产关键）

**痛点**：browser-use CLI 每次调用是独立 session —— 上一步 `open URL`，下一步 `click` 得重新打开一次页面，浏览器状态全丢。

**优化**：给每个 conversation 分配**稳定 session name**（`conv-{conversation_id}`），传给 browser-use CLI 的 `--session-name` 参数，让 CLI 复用同一 browser session。
- `_browser_session_name(conv_id)` 生成 deterministic name
- `_browser_session_config_path` 存 workspace-local 的 session config（`.krow/browser-use/conv-<id>.json`）
- 状态包括：使用的 browser（system Chrome vs Chromium）、profile 路径、CDP URL、headed vs headless

**面试话术**：
> "browser-use 每次调用默认是新 session —— 但 agent 一轮交互是几十次调用，每次打开新页面不现实。我做了 session 稳定化：一个 conversation 一个 session name，browser-use 用 --session-name 参数复用，配合 workspace-local 存 session config，让 agent 跨调用有连续的浏览器状态。"

### 6. Cookies 导入导出（登录态跨调用保持）

**痛点**：session 复用只保留 browser 状态，cookies 生命周期跟 process 绑 —— CLI 进程一退 cookies 就丢。

**优化**：每次 `close` 前 **export cookies** 到 workspace 里，下次 `open` 前 **import cookies** 回来。
- Cookies 存 `.krow/browser-use/conv-<id>.cookies.json`
- `browser_use_session_info` 工具让 agent 主动查这个路径 —— 生成可复用脚本时 agent 知道"要在 open 前 import cookies"

**收益**：即便是独立浏览器路径，用户手动登录一次后 cookies 就跨会话持久化，下次 agent 跑不用重登。

**面试话术**：
> "browser-use CLI 的 cookies 跟 process 生命周期绑，进程一退就丢。我做了 cookies 导入导出：close 前 export 到 workspace 的 cookies.json，open 前 import 回来。用户手动登录一次后，agent 后续调用都能保留登录态 —— 这个基本是让 use 从'能跑'到'能用'的关键改动。"

### 7. Structured 返回 + i18n 命令描述（前端友好）

**痛点**：browser-use CLI 输出是给人看的文本，agent 层需要**结构化数据**；前端 UI 需要**用户可读的中文描述**（不是 `click 3` 这种命令）。

**优化**：
- 返回统一走 `BrowserUseResult` pydantic model，结构化字段 agent 可靠消费
- 定义 `COMMAND_DESCRIPTIONS` i18n 表（中简/中繁/英），把 CLI 命令映射成"点击元素 [3]"、"打开 xxx"、"保存截图到 xxx" 这种用户可读描述
- 前端 tool card 显示的是描述，不是原始命令

**面试话术**：
> "browser-use CLI 输出是人读的，我做了结构化封装 —— BrowserUseResult 让 agent 层可靠解析；同时 COMMAND_DESCRIPTIONS 中英繁三语的映射表，把 CLI 命令翻译成用户可读的操作描述，前端 tool card 显示更友好。"

### 8. Session 元数据 API（可复用脚本生成）

**痛点**：agent 探索一个网站后经常要生成"自动化脚本"给用户跑（比如"每天自动打卡"），但脚本里怎么知道用哪个 session name / 哪个 profile / cookies 在哪？

**优化**：`browser_use_session_info` 工具返回 `BrowserUseSessionInfoResult{session_name, profile, headed, cdp_url, connect, browser, cookies_path}` —— agent 生成脚本时把这些元数据固化到脚本里，用户运行时**直接复用当时探索过的 session**（同一 browser、同一 cookies），登录态无缝。

**面试话术**：
> "agent 探索后要生成可复用脚本给用户 —— 但脚本得知道用哪个 session name、哪个 profile、cookies 在哪。我加了 browser_use_session_info 工具，把这些元数据一次性返回，agent 生成脚本时固化进去，用户跑脚本自动复用探索时的 browser 状态。"

---

## Part A 简历叙述模板

**保守版**（都能承担）：
> "参与 KroWork browser_use 工具的工程化优化，围绕 workspace 隔离安装、分步安装 + 进度可视化、系统 Chrome 优先探测、跨平台适配、session 稳定化、cookies 持久化、结构化返回、可复用脚本元数据 等方向让基于开源 browser-use CLI 的独立浏览器路径达到生产可用。"

**中等版**（做过其中几项）：
> "主要负责 browser_use 的 [session 复用与 cookies 持久化 / 分步安装 + 进度事件 / 跨平台安装适配] 模块，从最初一次调用一个新 browser session 优化到跨调用状态连续，让用户手动登录一次后 agent 后续调用无缝复用登录态。"

---

# Part B：browser_bridge 的合力实现

跟 use 的定位不同 —— bridge **不是替代 use**，而是解决 use 从根本上无法覆盖的场景（用户登录态、Chrome API 精细控制、事件推送、下载精准捕获）。团队合力从零设计和实现。

**代码位置**：
- `app/internal/browser_bridge/service.py`（665 行）
- `app/internal/browser_bridge/registry.py`（125 行）
- `app/internal/browser_bridge/preferences.py`（175 行）
- `app/routers/browser_bridge.py`（405 行）
- `app/internal/tools/browser_bridge_tool.py`（462 行）

## 核心架构

```
┌─────────────────────┐      ┌────────────────────────┐
│  Kro Bridge         │      │  krow-agent (FastAPI)   │
│  Chrome Extension   │      │                         │
│  装在用户 Chrome      │─WS──│  browser_bridge/        │
│  ├─ hello 握手       │      │    ├─ registry          │
│  ├─ handle command  │      │    │  (session/         │
│  └─ push events     │      │    │   browser/page)    │
└─────────────────────┘      │    ├─ service           │
                             │    │  (pending futures)  │
                             │    └─ preferences        │
                             │  routers/browser_bridge  │
                             │  tools/browser_bridge_tool│
                             └────────────────────────┘
                                        │
                                        ↓
                                    agent runtime
```

## 6 大关键能力

### 1. 命令请求 - 响应核心

- `send_command(browser_id, tool, args, command_id)`：uuid 塞 pending futures 表 → WS 发 `{type: "command", id, session_id, payload: {tool, arguments}}` → 阻塞 await future
- Extension 收 command → 用 Chrome API 执行 → 回 `{type: "result", id, payload}` → backend 按 id 唤醒 pending future

**关键设计**：pending futures 桥接"异步 WS event"和"同步 async/await"API —— agent 层完全不感知底层是消息通信。

### 2. Registry 三级索引

- `clients` (by session_id)：一个 extension 连接一份 —— 找 WS conn 用
- `clients_by_browser`：一个 browser profile 一份 —— agent 层稳定 ID，跨扩展重连不变
- `pages`：backend 生成的 `page_<uuid>` 作 key，稳定跨 read_state

**page_id 是 backend 生成的**（不是 chrome tab_id）—— agent 拿到稳定的 page_id 不受 tab 关-开影响，extension 内部用 tab_id 做实际操作。

### 3. 事件推送模式

Extension 监听 `chrome.webNavigation` / `chrome.tabs` API，主动推 `page_updated` / `page_closed` 事件 → backend Registry `upsert_page` / `remove_page`。

**好处**：agent 调 `list_pages` 直接读 Registry 内存，不需要真的问 extension；tab 变化不用轮询。

### 4. 完整 downloads pipeline

- Extension 用 `chrome.downloads` API 精准捕获下载事件（source_url / mime / captured_at 全有）
- **小文件**（< 200MB）：base64 编码一次性传
- **大文件**：streaming chunk upload 三段式（`/start` → `/chunk` × N → `/finish`），每 chunk base64 编码
- **Dedup**：同 base64 payload 只保留一份，blob URL 让位给真实 URL
- 落到 `workspace/browser/`，写 `downloads.jsonl` manifest
- `task_workspace_by_page` 绑定 page → workspace，下载自动落到对应 conversation 的目录

### 5. Extension 自动安装（跨平台）

`preferences.py` 里：
- **macOS**：写 `~/Library/Application Support/Google/Chrome/External Extensions/<EXTENSION_ID>.json` → Chrome 重启自动从 Chrome Web Store 拉扩展
- **Windows**：写注册表 `HKCU\Software\Google\Chrome\Extensions\<ID>\update_url`
- **CN / global CDN 双源**：`kcdn`（国内）+ `ap4r`（海外）双备份，按 region 选优先级
- **用户偏好持久化**：extension 接受/拒绝存 `~/.krow/config/browser-extension-preferences.json`，跨会话记住

### 6. 独有的浏览器能力（use 都没有）

| 能力 | 用途 |
|---|---|
| `network_start` / `network_log` | 抓页面 XHR/fetch 请求（做数据抓取 / 逆向调试） |
| `dropdown_options` / `select_dropdown` | 正确处理 `<select>` native 下拉（普通 click 处理不了） |
| `list_windows` | 多 Chrome window 管理 |
| `describe_element` | 单独获取某元素详细描述 |
| `execute_script` | 逃生舱：agent 传任意 JS，`chrome.scripting.executeScript` 执行 |
| Task workspace binding | 下载自动路由到 conversation workspace |
| HTTP debug endpoints | 完整 REST 端点镜像（`/click` / `/type` / `/wait-for` 全套），便于绕 agent 调试 |

## 安全信任模型

- **Discovery signature**：ping endpoint 校验固定 salt，防其他 localhost 服务被 extension 误连
- **Random token per process**：`discovery_token = uuid4().hex`，backend 重启即换
- **Session isolation**：所有 command 必须带 `browser_id`，extension 只跟自己的 browser 通信
- **Downloads size cap**：`DOWNLOAD_MAX_SIZE = 200MB`，防内存爆
- **Downloads path sanitize**：`_sanitize_download_name` + `_allocate_output_path` 防路径越权

---

## Part B 简历叙述模板

**保守版**（我参与了合力开发）：
> "参与 KroWork browser_bridge 的合力研发。基于 Chrome extension + WebSocket 长连接的自研桥架构，覆盖命令请求 - 响应（uuid + pending future 表）、Registry 三级索引（session/browser/page）、page 事件 push 订阅、downloads 流式上传三段式协议、Chrome extension 跨平台自动安装等能力，作为 browser_use 无法覆盖的登录态场景的补充路径落地。"

**中等版**（做过其中几个模块）：
> "参与 browser_bridge 核心模块研发，主要负责 [downloads pipeline / Registry / extension 自动安装 / 独有浏览器能力（network_log / dropdown_options / describe_element）] 的设计与实现，让 agent 能操作用户已登录的 Chrome，覆盖 browser_use 无法覆盖的登录态场景（Boss 直聘、内网 SSO、需登录的 SaaS）。"

---

# 两条路径的 fallback 关系

**agent SKILL 里的引导逻辑**：

```
首选 bridge：
  extension_status() → ready=true → 走 bridge

Fallback use：
  extension 没装 / 离线 → 退到 browser_use（独立浏览器）

场景区分：
  需要用户登录态（Boss / 内网 / SaaS）→ 必须 bridge
  纯公开数据爬取 / 需要隔离环境跑测试 → use 也行
```

面试问 "什么时候用哪个" 的标准答案：**首选 bridge（登录态天然覆盖），extension 没装 fallback use 兜底**。

---

# 面试话术三种口径

**做过的说"我做的"**：
> "我在 browser_use 侧做了 cookies 持久化 + session 元数据 API，让 agent 探索完能生成用户可复用的自动化脚本。"

**参与讨论 / 合力开发**：
> "我们讨论过 bridge 的 downloads pipeline 设计，最终选了 chunk streaming upload 而不是一次性 base64 —— 因为超过 200MB 的下载 base64 会把内存打爆。"

**技术洞察**：
> "我发现 bridge 相对 use 的核心价值是登录态覆盖 —— use 走独立 profile 无法覆盖 Boss 直聘、内网这些需登录场景。所以 bridge 不是替代 use，是补充。"

---

# 面试前准备的 3 件事

1. **画一张两条路径并存的架构图**：左边 use（agent → tool → subprocess → CLI → 独立浏览器），右边 bridge（agent → tool → service.send_command → WS → extension → 用户 Chrome）。**看图能说清楚 fallback 关系**。
2. **准备 "两块各一个具体故事"**：
   - use 侧：一个具体优化（比如 cookies 持久化的坑 / 分步安装的进度事件设计）
   - bridge 侧：一个具体模块（比如 downloads streaming upload 为什么拆三段式）
3. **答清楚"为什么两条路径并存"**：不是 bridge 淘汰 use，是**互补** —— 有 extension 优先 bridge（登录态好），没 extension 或需隔离环境 use 兜底

---

# 简历最终两段（简明版）

**放你简历上"KroWork" 项目下**，两个 bullet：

## use 段

> **browser_use 独立浏览器路径工程化优化**：分步安装 tool 配合实时进度事件让 150MB+ 首装可视化；按 conversation 稳定 session name 让 CLI 复用 browser profile 保持跨调用状态；额外导出 cookies 独立文件支撑 agent 生成的自动化脚本跨机器迁移。

## bridge 段

> **browser_bridge 浏览器桥**：Chrome extension + WebSocket 长连接与用户 Chrome 通信。参与后端请求 - 响应通信核心，基于 uuid + pending chan 表把 WebSocket 异步消息桥接成同步调用，覆盖独立浏览器无法处理的登录态场景。

## 关键面试守住点

**use 段追问准备**：
- "session 复用是怎么做的？" → CLI 有 `--session-name` 参数指向 browser profile 磁盘目录；每 conversation 分配稳定 name 让 CLI 复用同一 profile
- "cookies 持久化为什么单独做？" → CLI 内部 profile 里已经存了 cookies，独立文件是为了让 agent 生成的**跨机器可复用脚本**能带着登录态迁移

**bridge 段追问准备**：
- "pending chan 表是什么？" → `map[uuid] chan Result`：SendCommand 塞 chan 阻塞等，接收 goroutine 按 id 找 chan 塞值唤醒；桥接 WebSocket 异步消息 → 同步 API
- "怎么处理超时 / 断连 / race？" → 超时清 map（防内存泄漏）；断连遍历 map 全 fail（agent 立刻感知）；Go 里必须 mutex 保护（Python asyncio 单线程免锁）
- "为什么 chan buffer 是 1 不是 0？" → 让接收 goroutine 塞值动作永不阻塞 —— 就算 SendCommand 已经超时走了、chan 没人收，塞进去一个值就完事，不 block 整个接收循环

祝面试顺利。
