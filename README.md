# LingCoWork

**跟 AI 一起开工。** 一个本地跑的 Agent 工作站：Go 后端 + React 前端 + Electron 桌面壳，内置对话、工具调用、技能加载、RAG 检索、联网搜索、人工审批与浏览器桥。

> 仓库里 Go module 名仍是 `Interview-Agent`（历史遗留），产品在演进过程中扩展成了通用的 Co-work Agent，UI 侧统一叫 **LingCoWork**。

---

## 功能一览

- **Agent 对话** — 基于 [cloudwego/eino](https://github.com/cloudwego/eino) 编排，支持流式输出、多轮 thinking、工具调用。默认走 DeepSeek（OpenAI 兼容协议）。
- **工具集**（`internal/agent/tools`）
  - 文件系统：`fs`、`fs_ops`、`fs_chunked_write`
  - Shell 执行：`shell`（带破坏性命令审批门槛）
  - 联网：`web_search`（Tavily / Bocha 双源合并去重）、`web_fetch`
  - RAG：`rag_search`
  - 文档解析：`extract_document`、`docx`、`pptx`
  - 浏览器：`browser_bridge`、`browser_use`
  - HITL：`ask_user`（人工介入）
  - Skill 动态加载：`load_skill`
  - Workspace：`workspace`、`workspace_resolve`
  - OCR：`ocr_collector`
- **Skills**（`internal/agent/skills`）— docx / pdf / pptx / bosszp / browser-bridge / browser-use，按需加载，避免主 prompt 膨胀。
- **RAG 层** — chunker → embedding → indexer → retriever → sqlite 向量存储；离线索引用 `cmd/rag-index`，命令行检索用 `cmd/rag-search`。默认嵌入模型走阿里云 DashScope OpenAI-兼容接口，可替换为任意 `/embeddings` 端点。
- **审批 / HITL** — `internal/approval` + `internal/hitl`：破坏性动作会挂起等用户放行；`APPROVAL_FAST_MODEL`（默认 DeepSeek Chat）作为快速分类器决定是否走 auto 模式。
- **工作区隔离** — 每个会话都有独立的 `.workspace/<id>/`，工具的路径都在里面 resolve，防止越界。
- **前端**（`web/`）— React 19 + Vite + Tailwind + Zustand + Streamdown（Markdown 流式渲染）+ Shiki + KaTeX，附带 docx / pptx 预览器。
- **桌面壳**（`electron/`）— 只在 dev 期加载 Vite `:5173`，不负责起后端；纯壳。

## 技术栈

| 层 | 技术 |
| --- | --- |
| Backend | Go 1.26 · Gin · GORM · SQLite (glebarez/sqlite) · Eino |
| LLM | DeepSeek（eino-ext OpenAI adapter） |
| Frontend | React 19 · Vite 7 · TypeScript 5.9 · TailwindCSS 4 · Zustand |
| Desktop | Electron 40 |
| Storage | SQLite（对话 + 向量都在里面） |

## 目录结构

```
.
├── cmd/
│   ├── api/          # 主服务入口 (Gin, :9001)
│   ├── rag-index/    # 离线索引 CLI
│   └── rag-search/   # 命令行检索 CLI
├── internal/
│   ├── agent/        # Agent、LLM、tools、skills、prompts、runtimectx
│   ├── approval/     # 破坏性操作审批
│   ├── hitl/         # 人工介入 (ask_user)
│   ├── handler/      # HTTP handler (chat / conversation / project / workspace / approval)
│   ├── rag/          # chunker · embedding · indexer · retriever · store · vector
│   ├── repository/   # GORM 仓库
│   ├── service/      # 领域服务
│   ├── stream/       # SSE 流式与阶段追踪
│   ├── webfetch/     # 网页抓取
│   └── websearch/    # Tavily / Bocha 双源
├── web/              # React 前端
├── electron/         # 桌面壳
├── docs/
│   ├── eino/         # eino 框架笔记
│   ├── rag_docs/     # RAG 默认索引目录
│   └── interview-notes/
├── data/             # SQLite (interview.db / rag.db) - 首次运行自动创建
├── logs/dev/         # dev.sh 三个进程的日志
└── dev.sh            # 一键开发启动脚本
```

## 快速开始

### 依赖

- Go **1.26+**
- Node.js **20+**、`pnpm`
- macOS / Linux（Electron 部分只在 macOS 上验证过）

### 首次跑

```bash
cp .env.example .env
```

至少填一把 `DEEPSEEK_API_KEY`。想启用 RAG 就填 `EMBEDDING_API_KEY`；想让 agent 联网就填 `TAVILY_API_KEY` 或 `BOCHA_API_KEY`（两个都填能走 `region=both` 并发合并）。

```bash
./dev.sh
```

脚本会依次拉起：
- **backend** — `go run ./cmd/api`，监听 `:9001`
- **frontend** — `pnpm dev` in `web/`，固定 `:5173`
- **electron** — `pnpm start` in `electron/`，加载 `:5173`

打开 [http://localhost:5173](http://localhost:5173) 或直接用弹出的 Electron 窗口。

### 常用参数

```bash
./dev.sh --no-electron     # 只跑 backend + 浏览器前端
./dev.sh --no-frontend     # 只跑 backend (electron 会一起跳过)
./dev.sh --no-backend      # 前端 + electron，指向已跑的后端
./dev.sh --fresh           # 强制重新 go mod download / pnpm install
```

日志在 `logs/dev/{backend,frontend,electron}.log`。Ctrl-C 一次会清干净所有子进程。

### 单独构建

```bash
# backend
go build -o bin/api ./cmd/api

# frontend
cd web && pnpm build          # 产物在 web/dist/

# electron (dev-only 壳)
cd electron && pnpm start
```

## 环境变量

见 [`.env.example`](.env.example)。核心几组：

| 变量 | 作用 |
| --- | --- |
| `DEEPSEEK_API_KEY` / `LLM_BASE_URL` / `LLM_MODEL` | 主 LLM（DeepSeek OpenAI 兼容） |
| `LLM_ENABLE_THINKING` / `LLM_REASONING_EFFORT` | 是否启用思考 & effort（high/max） |
| `APPROVAL_FAST_*` | 审批 auto 模式的快速分类器（共用 `DEEPSEEK_API_KEY`） |
| `EMBEDDING_API_KEY` / `EMBEDDING_BASE_URL` / `EMBEDDING_MODEL` / `EMBEDDING_DIMENSIONS` | RAG 嵌入模型（默认 DashScope `text-embedding-v3`） |
| `RAG_DOCS_DIR` / `RAG_DB_PATH` / `RAG_CHUNK_SIZE` / `RAG_CHUNK_OVERLAP` | RAG 索引 & 检索参数 |
| `TAVILY_API_KEY` / `BOCHA_API_KEY` | 联网搜索。全空则 `web_search` 工具不注册 |

## RAG 离线索引

把要检索的文档丢进 `docs/rag_docs/`（可用 `RAG_DOCS_DIR` 改路径），然后：

```bash
go run ./cmd/rag-index                        # 全量索引
go run ./cmd/rag-search "query keywords"      # 命令行验证检索
```

服务运行时，agent 会通过 `rag_search` 工具走同一个 sqlite 向量库。

## 架构备注

- **Workspace 隔离** — 每个 conversation 一个 `.workspace/<id>/`，所有工具的路径都先经 `workspace_resolve`，越界的路径会被拒。
- **流式与阶段追踪** — `internal/stream` 分阶段发 SSE，前端可以拿到 "thinking / tool_call / tool_result / text" 等阶段标签。
- **审批门槛** — 默认 shell、fs 破坏性操作会挂起等审批；`APPROVAL_MODE=auto` 时快速分类器判断能不能自动放行；`ask_user` 工具走同一套人工介入通道。
- **Skills 动态加载** — Skill 是一组 prompt + 允许的工具白名单，用 `load_skill` 工具按需拉进上下文，主 prompt 保持精简。

## License

[MIT](LICENSE)
