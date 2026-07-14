# 前端右侧工作区文件预览的实现

> 每个 conversation 都有独立 workspace，agent 写的文件（简历分析报告、pptx、pdf、代码片段…）都落在里面。用户想在**不切窗口不下载**的情况下看到 agent 的产出 —— 右侧面板就干这个。

## 面试一句话答法

**三层前端 + 两个后端 endpoint**：

- **前端** `WorkspacePanel`（外壳 + 宽度拖拽）→ `WorkspaceTree`（文件树，dotfile 折叠、node_modules 折叠）→ `FilePreview`（按扩展名分发到 9 个 renderer）
- **后端**：`GET /workspace/tree` 返 flat 目录列表 + `GET /workspace/file` 返 512KB 上限的**文本内容 + kind 分类**（markdown / text / image / binary / unsupported）+ `GET /workspace/inline`（binary 类型直传 bytes，走 iframe 或 fetch as ArrayBuffer）+ `GET /workspace/download`（附件形式）
- **分发关键**：**前端按扩展名先识别 inline kind**（pdf / docx / pptx / mp4 / mp3 …）直接走 iframe / ArrayBuffer 消费，不 fetch 元数据；**其他文件走后端 kind** → text/markdown 走渲染，image 走 img，binary/unsupported 走"下载" fallback
- **触发刷新**：streaming 中 2s 轮询 + agent 每次 write/edit tool 成功后 SSE 帧带 workspace_changed → 增 `filesVersion` → tree 自动重拉

**核心难点是"渲染多样性"**：docx / pptx 客户端真渲染（`docx-preview` / `@aiden0z/pptx-renderer` 动态 import），md 走 `MessageBody` 复用聊天渲染，code 走 shiki 高亮，csv 走表格。**不下载在浏览器里全搞定**。

---

## 需求 / 核心难点

Agent 写一份 pptx / pdf / 报告，用户如果得**下载**才能看，体验断了。所以 UX 目标：**产出即预览**。

技术上难在**格式多样性**：

| 格式 | 挑战 |
|---|---|
| markdown | 要跟聊天里的 markdown 渲染一致（复用组件） |
| 代码 / 配置 | 语法高亮 + 行号 |
| CSV / TSV | 渲染成表格而不是长字符串 |
| 图片 | 直接 img，无需 fetch 内容做 base64 |
| PDF | 浏览器原生 pdf viewer，iframe 直嵌 |
| **docx** | 二进制 zip 装 XML，浏览器不认，需要**客户端解压 + 渲染** |
| **pptx** | 同上，还要处理多 slide 布局 |
| 音视频 | `<video>` / `<audio>` 原生标签，但需要正确的 mime type |
| 其他二进制 | fallback 到下载 |

**边界要求**：
- 大文件不能把浏览器搞挂（`> 512KB` 截断 + 尾部提示）
- 大 deck 不能一次性渲染完（pptx 用 windowed 模式）
- 敏感文件（`.env` / private key）不能被 inline 服务成 html 让浏览器猜类型执行

---

## 三层前端架构

```
WorkspacePanel（外壳）
  ├─ 宽度拖拽（pointer 事件）
  ├─ streaming 中 2s 轮询刷新 tree
  ├─ 会话切换时清空 previewPath
  └─ 内部条件渲染：
       - 未选文件 → WorkspaceTree
       - 已选文件 → FilePreview
```

**`WorkspacePanel`** 只做外壳 + 拖拽 + 生命周期，不管具体渲染 —— 决定用户看到 Tree 还是 Preview。

**`WorkspaceTree`** 展示文件树，处理"哪些默认折叠"（`.` 开头的目录、`node_modules` / `.git` / `dist` / `.venv` / `.cache` 等一堆），点击文件调 `openFile(path)` 切到 Preview 模式。

**`FilePreview`** 按扩展名分派到 9 个 renderer 之一。

---

## 状态管理（Zustand）

`useWorkspaceStore` 集中管所有 workspace UI 状态：

- `panelOpen`（面板整体开关）
- `previewPath`（当前预览的文件路径，null 时展示 Tree）
- `previewWidth`（宽度，360-760 clamp）
- `switcherOpen`（切文件 Overlay 开关）
- `filesVersion`（数字版本号，每次 +1 触发 tree 重拉）
- `resetConversationState`（切会话时清 previewPath + 增 version）

**持久化**：只 persist `panelOpen` 和 `previewWidth`（用户体感偏好），**不 persist `previewPath`** —— 切会话 / 刷新时不应该"上次看啥现在还看啥"（那个文件可能压根不在新 workspace 里）。

**`filesVersion` 是刷新触发器**：Tree 组件用 `useEffect(() => fetch(...), [filesVersion])`，任何地方调 `refreshFiles()` 都会让 tree 重拉。事件驱动。

---

## 后端：4 个 endpoint 各司其职

`internal/handler/workspace.go`：

| endpoint | 用途 | 返回 |
|---|---|---|
| `GET /conversations/:id/workspace/tree` | 列文件（flat + 相对路径） | JSON `{workspace, entries[]}` |
| `GET /conversations/:id/workspace/file?path=X` | 读文本内容（≤ 512KB，含 kind 判断） | JSON `{path, kind, content, truncated, size}` |
| `GET /conversations/:id/workspace/inline?path=X` | 二进制内联送，带白名单 mime | 原始 bytes + `Content-Disposition: inline` |
| `GET /conversations/:id/workspace/download?path=X` | 附件下载 | 原始 bytes + `Content-Disposition: attachment` |

**tree 的设计选择**：**flat 列表 + 相对路径**（不是嵌套 JSON 树）。前端拿到自己在 `buildWorkspaceTree` 里按 `/` 分层。原因：
- flat 网络传输更省 —— JSON 里嵌套树光符号就多一倍
- 前端 sort 逻辑（目录先、按字母）比后端做更灵活
- 上限 500 条目防止一次拉爆

**file 的 kind 分类**：后端 `classifyExt(ext)` 返 5 种：`markdown / text / image / binary / unsupported`。前端拿到 kind 分派渲染器 —— **除了扩展名前端能自己识别的（pdf/docx/pptx/media），其他都靠后端 kind**。

**inline vs download 分工**：
- inline：浏览器**当前 tab 内嵌打开**（pdf 走 iframe pdf viewer、video 走 `<video>` 标签、docx/pptx 前端 fetch as ArrayBuffer）
- download：**点了下载**（binary / unsupported fallback）

**inline 的安全边界**：只允许**白名单 mime 类型**内联（`.pdf / .docx / .pptx / .mp4 / .webm / .mov / .mp3 / .wav / ...`），带 `X-Content-Type-Options: nosniff` 头挡住浏览器把它当 html 猜执行。这个白名单是**故意窄**的 —— 阻止 `.html / .svg / .js` 之类被内联执行的安全隐患。

---

## FilePreview 的分派逻辑

`FilePreview.tsx` 里两级分派：

### 第一级：前端按扩展名识别 inline kind

```typescript
type InlineKind = "pdf" | "docx" | "pptx" | "video" | "audio";

function detectInlineKind(path: string): InlineKind | null {
  const ext = path.toLowerCase().split(".").pop();
  if (ext === "pdf") return "pdf";
  if (ext === "docx") return "docx";
  if (ext === "pptx") return "pptx";
  if (VIDEO_EXTS.has(ext)) return "video";
  if (AUDIO_EXTS.has(ext)) return "audio";
  return null;
}
```

**inline kind 直接跳过后端元数据 fetch** —— 浏览器 / 客户端渲染库自己知道怎么消费 bytes：

```typescript
if (inlineKind) {
    // 不 fetch /file 元数据，直接渲染
    // PDF: iframe src=workspaceInlineURL
    // docx/pptx: 组件内部 fetch as ArrayBuffer
    // media: <video src=workspaceInlineURL>
    return;
}
```

### 第二级：其他文件走后端 kind

Fetch `/workspace/file?path=X` 拿 `kind + content`：

```typescript
{file.kind === "markdown" && <MarkdownRenderer content={file.content} />}
{file.kind === "text" && (
    isTablePath(file.path)
      ? <TablePreview content={file.content} path={file.path} />
      : <CodePreview content={file.content} fileName={file.name} />
)}
{file.kind === "image" && <ImageRenderer conversationId={conversationId} path={file.path} />}
{(file.kind === "binary" || file.kind === "unsupported") && <UnsupportedRenderer .../>}
```

**这个两级设计的关键**：**能少一次网络就少一次**。pdf/docx/pptx/media 已知需要 bytes 消费，元数据 fetch 是浪费；而 text 类必须先探 kind 才能决定渲染器。

---

## 9 个 Renderer 各一小段

### `MarkdownRenderer` — 复用聊天渲染

**9 行代码**：直接把 content 塞进 `<MessageBody>` 组件（聊天里 assistant 消息的 markdown 渲染器）。**复用是关键**：workspace 里的 md 和聊天里的 md 长得**必须一模一样**，用户才不会困惑。

### `CodePreview` — Shiki 语法高亮

- 用 [Shiki](https://shiki.style/) 做高亮（vscode 同款高亮引擎，output 是纯 HTML）
- `resolveLanguage(fileName)` 按扩展名映射到 Shiki 支持的语言
- 高亮 async 完成前先渲染纯 `<pre>` fallback，避免 blocking
- 用 `dangerouslySetInnerHTML` 塞 highlighted HTML（Shiki 输出的 HTML 已经是转义安全的）
- 带行号

### `TablePreview` — CSV / TSV 转 HTML 表格

`isTablePath` 判 `.csv` / `.tsv` → 走 `TablePreview`；否则 CodePreview。

按行 split → 按分隔符 split（逗号或 tab）→ 渲成 `<table>`，第一行加粗当表头。

### `ImageRenderer` — 单标签 img

**24 行**：`<img src={workspaceInlineURL(...)} alt={name} />` + max-width 100% 撑起来完事。

**用 inline URL 而不是 base64 data URI**：base64 会把整张图 encode 塞进 DOM 树 —— 一张 1MB 图变成 1.3MB DOM，React 重渲染时抖。inline URL 走浏览器原生图片管道，缓存 / 并发下载 / 内存管理都是浏览器优化过的。

### `PdfPreview` — iframe 硬吃

**22 行**：`<iframe src={workspaceInlineURL(...)}>` —— 浏览器自带 PDF viewer。不需要 pdf.js 之类 800KB 的库。

代价：iframe 有 sandbox 边界，跨 frame 通信不方便（比如"跳到第 N 页"这种做不到）。够用了不管。

### `MediaPreview` — video / audio

原生 `<video controls>` / `<audio controls>` + `workspaceInlineURL` 做 src。浏览器负责 codec 支持、拖动进度、音量控制。

### `DocxPreview` — 客户端真渲染

关键难点：**docx 是二进制 ZIP 里装 XML**，浏览器不认。

方案：
1. `fetch(workspaceInlineURL)` 拿 ArrayBuffer
2. **动态 import** `docx-preview`（~500KB 的重库，只在用户真点开 docx 时才拉）
3. 喂给 `renderAsync(buffer, container, undefined, options)`
4. 库解压 zip + 解析 XML + 渲染成 DOM（表格 / 列表 / 图片全支持）
5. **Symbol/Wingdings PUA 字符修复**：docx 列表项目符号在 Symbol 字体下是 PUA 字符（0xF0xx 区段），浏览器没装 Symbol 字体会渲染成豆腐块 □。**手动 walker 遍历 DOM 把这些 PUA 字符替换成 Unicode 等价**（• ▪ ○ ✔ 等）+ 把 Symbol/Wingdings font-family 改成 inherit。
6. 加载失败给"下载查看"按钮兜底

**这个 PUA 修复是我们从 PentaLoom 学的坑**（原文注释里就写了"krow 趟过的坑"），现实中 word 里 90% 的列表都会踩。

### `PptxPreview` — 客户端真渲染 + windowed 模式

**类似 DocxPreview** 但用 `@aiden0z/pptx-renderer`：

1. fetch ArrayBuffer
2. 动态 import 库（~1.5MB，只在用户真点开 pptx 时拉）
3. `new PptxViewer(container, options)` + `viewer.open(buffer, { renderMode: "list", listOptions: { windowed: true, batchSize: 4 } })`
4. **windowed 模式关键**：大 deck（100+ slides）不会一次性渲染 —— 按视口按需渲染，超出视口的 slide 释放。100 slide 的 pptx 内存占用可能几百 MB，windowed 之后只留可视区几张。
5. 切文件时**必须 `viewer.destroy()`**，否则上一个 viewer 的 DOM 还挂着内存泄漏
6. **动态 import + 组件切换 unmount = 首次点开慢 1-2s，之后瞬开**（chunk 缓存）

### `UnsupportedRenderer` — 下载兜底

39 行：显示文件名 + 大小 + reason + "下载"按钮（走 `workspaceDownloadURL`）。**"不支持"要用户体感清楚**：不是转圈卡住，是明确"这种格式我们不渲染，点这里下载"。

---

## 刷新策略：轮询 + 事件驱动混合

用户和 agent 同时在改 workspace，前端要及时反映。三层刷新触发：

### 1. Streaming 中 2s 轮询

`WorkspacePanel` 里：

```typescript
useEffect(() => {
    if (!panelOpen || !conversationId || !streaming) return;
    refreshFiles();
    const interval = setInterval(refreshFiles, 2000);
    return () => clearInterval(interval);
}, [conversationId, panelOpen, refreshFiles, streaming]);
```

**只在 streaming 时轮询**：agent 不跑了就不轮询，省 CPU + 省带宽。

### 2. SSE 事件驱动

Agent 每次跑成功 write / edit / mkdir / rm / mv / run_command 之类**可能改文件的工具**，SSE 的 tool_result 帧到前端时 `mayAffectWorkspace(name)` 判定为 true → `refreshWorkspaceFiles()` → 增 `filesVersion` → tree 重拉。

`useChatStream.ts` 里：

```typescript
if (status === "ok" && mayAffectWorkspace(f.name)) {
    onWorkspaceChanged?.();  // 触发 refreshFiles
}
```

这条比 2s 轮询快，通常 tool_result 到达时文件已经落盘了，refresh 立刻能看到。

### 3. 会话切换 reset

`WorkspacePanel` 里：

```typescript
useEffect(() => {
    if (previousConversationIdRef.current === conversationId) return;
    previousConversationIdRef.current = conversationId;
    resetConversationState();   // 清 previewPath + 增 filesVersion
}, [conversationId, resetConversationState]);
```

切会话时 previewPath 清空（切到 Tree 视图）+ tree 重拉（新会话有新的 workspace）。

---

## FileSwitcherOverlay：快速切文件

点击顶部路径栏时弹出 —— **不需要点回 Tree** 就能在当前预览里切到别的文件。类似 VSCode 的 `Ctrl+P`。

实现：一个模态 overlay，列 tree 里所有文件（compact 模式），点了就 `openFile(newPath)`，overlay 关闭。用户在预览深度模式下也能快速切文件。

---

## 面试可能追问

**Q：为什么不用 iframe 硬嵌所有格式？**

A：iframe 只对**浏览器原生支持**的格式管用（PDF、图片、text/plain）。docx/pptx 浏览器不认，iframe 会下载或显示"不支持"。所以：
- **PDF** 走 iframe（原生 pdf viewer 免费用）
- **docx/pptx** 必须客户端库解压渲染（`docx-preview` / `@aiden0z/pptx-renderer`）
- **图片/媒体** 走原生 `<img>` / `<video>`，比 iframe 更轻

**Q：docx-preview 和 pptx-renderer 加载慢怎么办？**

A：三件事：
1. **动态 import** —— 只在用户真点开 docx/pptx 时才拉那 ~500KB / 1.5MB 的库
2. **组件卸载时 destroy** —— 释放上一个 viewer 的 DOM + 内存
3. **失败给下载兜底** —— 加载失败/渲染失败显示"下载查看"，不阻塞用户

首次点开 1-2s 是可以接受的（用户点开 docx 本来就有心理预期"这需要处理一下"），之后 chunk 缓存瞬开。

**Q：为什么 tree 是 flat 而不是嵌套？**

A：三个原因：
1. **网络体积小** —— JSON 嵌套树光括号符号就多一倍
2. **前端 sort 灵活** —— 目录先 / 按字母 / 折叠状态这些都是 UI 决策，后端不管
3. **上限简单** —— 500 条 flat 列表好做 `LIMIT`，嵌套树的"深度爆炸"更难限

代价：前端要跑一次 `buildWorkspaceTree` 把 flat 按 `/` 分层。O(N) 一遍完，可以忽略。

**Q：inline 白名单为什么这么窄？**

A：**SSRF-style 攻击面**：如果允许 `.html / .svg / .js` 内联，浏览器可能把它当 HTML 解析执行 —— **在同源里跑攻击者控制的脚本**能拿到 session cookie、localStorage 等。

白名单只放**浏览器绝对不会执行的格式**（pdf 独立 viewer / 二进制 zip / 媒体），加上 `X-Content-Type-Options: nosniff` 双保险。

**Q：512KB 文件截断是不是太小了？**

A：**LLM 上下文视角选的**：主要用户是 agent 自己 read_file，一次读进上下文最多几百 KB 才不炸。前端预览沿用同一个上限保持一致 —— 512KB 长文本前端渲染也基本流畅。

代价：超过 512KB 的文件（比如大 CSV / 日志）只能看开头。**加尾部提示 + 提供下载按钮**兜底。

**Q：切文件时 pptx viewer 不 destroy 会怎样？**

A：内存泄漏 —— pptx-renderer 内部持有 chrome-pdf-like 的画布 + 图片资源，切文件时 React 卸载组件但 viewer 对象还挂着。切了 10 个 pptx 内存增几百 MB，浏览器就卡。所以：

```typescript
useEffect(() => {
    // ... 渲染新的 pptx
    return () => {
        viewerRef.current?.destroy();   // cleanup
        viewerRef.current = null;
    };
}, [conversationId, path, projectId]);
```

**Q：CodePreview 用 shiki 会不会太重？**

A：Shiki 默认全语言 bundle 是 MB 级 —— 我们**只按需 import 语言**（`resolveLanguage(fileName)` 返回具体 lang name，Shiki 内部动态 load 只该 lang 的语法定义）。首次某语言几百 KB 但缓存后瞬开。

比 `highlight.js` 好的地方：Shiki 用 TextMate 语法（vscode 同款），准确度高很多。

**Q：MarkdownRenderer 直接 delegate 给 `MessageBody` 会不会耦合？**

A：**故意的耦合**。workspace 里的 md 和聊天里的 md 长得**必须一致**（同款代码块高亮、同款表格、同款 GFM、同款 KaTeX），用户看两个地方感觉是同一个渲染器才不困惑。

真要解耦得两边同步维护，反而更累。**共用一个组件是设计约束的显式化**。

**Q：workspace_changed SSE 事件怎么定义"可能改文件的工具"？**

A：`useChatStream.ts` 里维护一个白名单 + 关键词兜底：

```typescript
const WORKSPACE_TOOL_NAMES = new Set([
    "write_file", "edit_file", "create_file", "delete_file",
    "rename_file", "move_file", "mkdir", "run_command", "shell",
]);

function mayAffectWorkspace(name?: string): boolean {
    if (!name) return false;
    if (WORKSPACE_TOOL_NAMES.has(name.toLowerCase())) return true;
    // 兜底：名字里含 file / workspace / shell / command
    return normalized.includes("file") || normalized.includes("workspace") || ...
}
```

**宁可多刷不能漏刷** —— 刷一次 tree 才几十 ms，漏刷会让 UI 显示滞后。

**Q：怎么处理 workspace 是个 git 仓库有 .git 目录的情况？**

A：`.git` 已经在 `COLLAPSED_BY_DEFAULT` 集合里，Tree 默认不展开。加上"以 `.` 开头的目录默认折叠"这个通用规则，`.git / .venv / .cache / node_modules` 全都不占视觉空间，但用户点了能看到。

---

## 一句话小结

**前端**：外壳（拖拽 / 生命周期）+ Tree（flat 树 + dotfile 折叠）+ Preview（两级分派：前端识别 inline kind → 元数据 fetch 走 kind → 9 个 renderer）+ Zustand 状态（`filesVersion` 事件触发）。

**后端**：4 个 endpoint 各司其职（tree 列 / file 读文本 + kind / inline 传 bytes / download 附件）+ 白名单 mime 挡住 SSRF-style 攻击。

**核心哲学**：**"能少一次网络就少一次" + "浏览器原生能干的别自己写"**。docx/pptx 是不得不客户端渲染 → 用成熟库 + 动态 import + destroy cleanup 三件套压低成本。
