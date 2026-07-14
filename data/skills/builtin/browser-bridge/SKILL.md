---
name: browser-bridge
description: 用用户日常 Chrome 浏览器完成任务的**首选路径**——只要用户装了 Kro Browser Bridge 扩展且已连接，任何浏览器任务都优先走这条（复用登录态、用户能看到 agent 现场操作）。触发场景：用户说"打开网站"/"帮我搜"/"看下这个网页"/"在 X 网站做 Y"等**任何**浏览器场景都先探测 extension_status，ready=true 就用这个。ready=false 才降级到 browser-use。适用工具：browser_bridge。
---

# Skill: browser-bridge（用户 Chrome 桥接 · 首选浏览器路径）

驱动用户**自己的** Chrome 完成任务。**任何浏览器任务的第一步都是探测扩展**，能连就用这条，连不上才走 browser-use 兜底。

## 决策优先级（硬规则）

看到用户提及浏览器任务：

1. **第一步永远是** `browser_bridge(action='extension_status')`
2. `ready=true` → 用 bridge 完成整个任务
3. `ready=false` → 降级到 `browser-use`（load 那个 skill）
4. **不要**问用户"要不要装扩展"，也**不要**尝试重试 extension_status；一次探测决定走哪条

bridge 是首选，因为它：
- 复用用户已登录状态，避免让用户在 agent 上再登一次
- 用户能看着自己 Chrome 里 tab 一个个动，可解释性强
- 不需要额外启动 chromium 进程

## 工作流

1. **探测**：`browser_bridge(action='extension_status')` → 检查 ready
2. **拿 browser_id**：`browser_bridge(action='list_sessions')` → 一般只有一个 session；把 browser_id 记住，后续全带
3. **看已有 tab**：`browser_bridge(action='list_pages', browser_id=...)` → 用户可能已经打开了要用的页面（比如 GitHub），直接 focus_page 复用
4. **新开 tab**（如需要）：`browser_bridge(action='open_tab', browser_id=..., url=...)` → 拿 page_id
5. **观察**：`browser_bridge(action='read_state', browser_id=..., page_id=...)` → 编号 markdown
6. **操作**：click / type / press 按 index
7. **验证**：每次交互后再 read_state，看 URL / DOM 是否变化
8. **收尾**：**不要主动 close_tab**，用户的 tab 保留给用户；除非用户明说"关掉"

## 硬性纪律

### 一 · 操作成功 ≠ 业务成功

登录、下单、支付这些关键动作后必须再 read_state 验证。看到明确成功标识（订单号、URL 跳转、用户头像出现）才能报"已完成"。

### 二 · 用户阻断立刻交回

CAPTCHA / 短信码 / 邮箱验证 / 银行支付 / "我同意"条款 —— 全部**不要替用户点**。停下来告诉用户当前 URL + 阻断原因 + 需要用户做什么。

### 三 · index 只在最近一次 read_state 之后有效

Bridge 端的 index 跟 use 端语义一致：DOM 一动就废。每次操作前重新 read_state。

### 四 · page_id 可能过期

- 用户手动关掉 tab → page_id 立即作废（收到 page_removed 事件 registry 清掉）
- 扩展重连 → session_id / browser_id 可能变，最好重新 list_sessions 拿新的
- 报 "page_id not found" 时不要重试，重新 list_pages 拿现有 tab

### 五 · 这是用户的真实浏览器

- 不要关别人的 tab（除非用户明说）
- 不要用 execute_script 之类跑破坏性 JS
- 不要在用户已登录的敏感网站上做超出请求范围的操作（比如用户说"看 GitHub 通知"，不要顺手 follow 别人）
- 读页面前先确认这个 tab 是不是 agent 打开的 / 用户明确要操作的

## Action 速查

| Action | 参数 | 用途 |
|---|---|---|
| `extension_status` | — | 探测扩展有没有连上；ready=false 就切 browser_use |
| `list_sessions` | — | 列出所有已连接的扩展实例；拿 browser_id |
| `list_pages` | browser_id | 列出该浏览器所有 tab；拿 page_id / URL |
| `open_tab` | browser_id, url, active? | 新开 tab 加载 URL |
| `focus_page` | browser_id, page_id | 把 tab 提到前台 |
| `close_tab` | browser_id, page_id | 关一个 tab（**谨慎，这是用户 Chrome**） |
| `read_state` | browser_id, page_id | 拿编号 markdown |
| `click` / `hover` / `dblclick` / `rightclick` | browser_id, page_id, index | 点击变体 |
| `type` | browser_id, page_id, index, text | 填输入框 |
| `press` | browser_id, page_id, key, index? | 键盘按键；index 省略作用于整页 |
| `scroll` | browser_id, page_id, x, y, index? | 相对滚动，正 y = 向下 |
| `wait_for` | browser_id, page_id, timeout_ms? | 等待页面稳定（默认 10s 上限） |
| `go_back` / `reload` | browser_id, page_id | 浏览器导航 |
| `extract` | browser_id, page_id, index, include_html? | 拿元素文本（可选 HTML） |
| `describe_element` | browser_id, page_id, index | 拿元素稳定 selector / 属性，用于升级 index → CSS |
| `execute_script` | browser_id, page_id, script | 跑任意 JS，返回值必须 JSON 可序列化 |

## execute_script 硬约束（否则静默返 null）

写脚本时**必须**遵守，不然扩展会静默吞异常返 `value: null`，无法诊断：

1. **单行、无换行、无缩进** —— 脚本源码里不能出现 `\n`。多行写法（`\n` 分行 + 空格缩进）**必挂**。语句用 `;` 分隔全部塞到一行。理由：链路某一步对换行处理有 bug，参考项目一律用字符串拼接构建单行 JS
2. **IIFE + 显式 return** —— `(function(){...return X;})()`。裸表达式 `"hello"`、`1+1` 一律返 null
3. **不用双引号 `"`** —— 全部用单引号 `'`；对象字面量 key 不加引号（`{title:x}` 而不是 `{"title":x}`）。JSON.stringify 返回值里自然有 `"`，那是无所谓的
4. **CSS 选择器里能不用嵌套引号就不用** —— `[class*=user-name]` 而不是 `[class*="user-name"]`（`user-name` 是合法 CSS3 ident）。含 `/` `:` 等非 ident 字符的属性值不得已才用嵌套引号
5. **只读 DOM 及其 expando** —— `document.querySelector(...)` 拿到的元素上，Vue 挂的 `__vue__`、React 挂的内部 fiber 都能读（这些是 DOM 元素属性）。但 `window.某某` / `_PAGE` 之类的 page world 全局，扩展在 isolated world 大概率读不到，用 `try{}catch(e){}` 兜底
6. **返回体控制在 KB 级** —— 一次抽取超过 20 条明显复杂对象容易返 null。分批 `slice(0, 10)`
7. **null 一次即停** —— 返回 null 一次先 `wait_for` 2 秒重试；仍 null 停下报告，不要死循环猜

**正例**（参考项目风格，压单行）：
```js
(function(){var n=document.querySelectorAll('.job-card-wrap').length;return n;})()
```

**反例（会挂）**：
```js
(function(){
  var n = document.querySelectorAll('.job-card-wrap').length;
  return n;
})()
```
形式上是 IIFE，但**有换行** → 静默 null。

## 失败处理

| 现象 | 措施 |
|---|---|
| extension_status.ready=false | 立刻切 browser_use，不要问用户 |
| tool 报 "browser_id not connected" | 扩展掉线了，重新 extension_status 确认；仍不行 → 提示用户重装扩展 / 检查 Chrome 是否在运行 |
| tool 报 "page_id not found" | tab 被用户或其它进程关了，重新 list_pages 拿最新 tab |
| tool 报 "timed out" | 页面卡死或扩展忙；不要盲重试，先 read_state 看状态 |
| index 越界 | 页面变了，重新 read_state |

## 不该做

- 不查 extension_status 就直接调其它 action —— 扩展没连就一律 fail
- 猜 browser_id 或 page_id —— 一律先 list_sessions / list_pages
- 主动关用户的 tab —— 除非用户明说
- 用 bridge 处理"要 workspace 落盘"的任务 —— bridge 不支持 screenshot 存盘，那种走 browser_use
