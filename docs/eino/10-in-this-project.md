# 10 · 本项目落地案例整合（面试可讲的完整故事线）

前 9 章每章都点了"本项目怎么用的"，这一章把它们**串成一条完整故事线**，方便面试的时候按"介绍项目 → 讲一个亮点 → 深入"的节奏往下钻。

**核心信息**（一分钟版）：

> Interview-Agent 是一个 Go 实现的面试准备/生产力助手。后端用 CloudWeGo eino v0.9.1 + eino-ext claude 做 LLM 编排，前端 React + SSE。核心拓扑是 ADK 里的 Supervisor + AgentAsTool + 两个 sub-agent（DeepAgent 预制 + 自写 ChatModelAgent）。做过一次从 flow/agent/react 到 ADK 的架构迁移；接入了 HITL 审批；针对"模型谎称调用工具"做了结构化历史回放修复。

---

## 1. 项目定位

- **用户视角**：帮用户做面试准备（简历分析、题库生成、学习计划）+ 通用生产力（读文档、浏览网页、找工作、任意本地文件读取）
- **技术视角**：SSE 流式对话 UI + 多 Agent 后端 + 本地工作区文件系统 + Chrome 扩展浏览器桥 + Boss 直聘 skill + RAG 检索
- **正在做的**：Milvus + OpenAI embedding 的 RAG 检索、shell 命令执行工具

---

## 2. 目录结构与分层

```
Interview-Agent/
├─ cmd/api/main.go              服务入口：装配所有 repo/service/handler/agent/tools
├─ internal/
│   ├─ agent/
│   │   ├─ agent.go             残留：NewReActAgent（老路线，未使用）
│   │   ├─ adk_agent.go         ADK 拓扑装配：Supervisor + DeepAgent + JobSearch
│   │   ├─ prompts/             各 agent 的 system prompt（纯字符串常量）
│   │   ├─ tools/               所有业务 tool（fs / browser / skill / shell / rag_search）
│   │   ├─ toolerr/             ToolMiddleware：错误转 tool result
│   │   ├─ skills/              技能包加载（bosszp / 面试题库）
│   │   ├─ browserbridge/       Chrome extension 桥服务
│   │   ├─ browseruse/          Playwright headless 浏览器
│   │   ├─ llm/                 ChatModel 构造
│   │   └─ contextkey/          ctx value key 集中处
│   ├─ approval/                HITL 审批：ToolMiddleware + PendingStore + Policy
│   ├─ rag/retriever/           Milvus + OpenAI embedding 的 RAG 检索
│   ├─ handler/                 HTTP handler（chat / conversation / project / workspace）
│   ├─ service/                 业务服务（ChatService / ConversationService / ...）
│   ├─ stream/                  SSE 编码 + ADK 事件消费 + RunCollector
│   ├─ repository/              GORM + sqlite 持久化
│   └─ config/
├─ web/                         React + Vite 前端
├─ docs/eino/                   本文档
└─ .workspace/                  运行时用户工作区目录
```

**分层原则**：`handler → service → repository/agent → eino` 单向依赖；`agent` 层自包含（业务不知道 eino 的存在，只知道 `service` 层暴露的 API）。

---

## 3. 一次 chat 的完整生命周期

```
┌─────────────────────────────────────────────────────────────────────────┐
│ [1] POST /chat/:id  {"message": "..."}                                    │
│      handler/chat.go::Chat                                                 │
│              │                                                              │
│              ▼                                                              │
│ [2] chat.Start(ctx, convID, userMsg, projectID)                            │
│      - convRepo.Upsert / SetProjectID                                      │
│      - msgRepo.List(convID) → prior rows                                   │
│      - toSchemaMessages(convID, prior) → []*schema.Message                 │
│        （orphan tool_call filter 在这里，见 04 § A修复）                     │
│      - prepend workspaceContext SystemMessage                              │
│      - append userMsg 作 UserMessage                                       │
│      - msgRepo.Append(user row)                                            │
│      - manager.Create(convID) → buf                                        │
│      - pending.Clear(convID)（清老中断态）                                    │
│      - go runAgent(ctx, convID, history, buf)                              │
│      - 返回 buf 给 handler → SSE 流开始                                     │
│              │                                                              │
│              ▼                                                              │
│ [3] runAgent（goroutine）                                                    │
│      - context 注入：convID / buf / toolerr.Registry                        │
│      - collector := stream.NewRunCollector()                               │
│      - convRepo.SetAgentStatus(convID, "running")                          │
│      - iter := runner.Run(ctx, msgs, adk.WithCheckPointID(convID))         │
│      - consumeAndPersist(ctx, iter, sink, buf, collector)                  │
│              │                                                              │
│              ▼                                                              │
│ [4] stream.ConsumeADKEvents（在 goroutine 内）                               │
│      for {                                                                  │
│        ev := iter.Next()                                                    │
│        if ev.Err → return err                                              │
│        if ev.Action.Interrupted → emitApprovalRequired → sink.Record       │
│        if ev.Output.IsStreaming → drainAssistantStream                     │
│                                    - 每 chunk 发 SSE "thinking"/"text"      │
│                                    - 末尾 ConcatMessages → 发 tool_call     │
│                                    - collector.OpenTurn                    │
│        if ev.Output.Role=Tool → emitToolResult                             │
│                                  - 发 SSE tool_result（含 toolerr 二次查表） │
│                                  - collector.AttachToolResult              │
│      }                                                                      │
│              │                                                              │
│              ▼                                                              │
│ [5] persistRun(convID, collector)                                          │
│      - 遍历 collector.Turns()                                               │
│      - padMissingToolResults 补齐 canceled tool row                          │
│      - 构造 []*model.Message：assistant(带 ToolCalls JSON) + tool × N       │
│      - dual-write 最后 assistant Extra（legacy tools + sub_events）          │
│      - msgRepo.AppendMany(rows) 单事务原子写                                 │
│              │                                                              │
│              ▼                                                              │
│ [6] FinalizeOK(buf) → SSE close → convRepo.SetAgentStatus(idle)             │
└─────────────────────────────────────────────────────────────────────────┘
```

**如果第 4 步遇到 Action.Interrupted**：`SetAgentStatus(waiting_approval)`，buf.Finish，等用户点批准 → POST 到 `/approval/:cid/:iid` → `chat.Resume` → `runner.ResumeWithParams` → 新 buf → 前端 `GET /chat/:cid` 重连订阅新流。完整链路见 [08 §8](08-multi-agent.md)。

---

## 4. 关键设计决策（面试点集合）

| # | 决策 | 章节 | 一句话原因 |
|---|---|---|---|
| 1 | 走 ADK 路线，不走 `flow/agent/react` | [07](07-react-agent.md) [08](08-multi-agent.md) | 多 sub-agent + 事件流 + HITL 一等公民 |
| 2 | Sub-agent 挂载用 AgentAsTool，不用 Transfer | [08 §6](08-multi-agent.md) | 上下文隔离、UI 天然挂父子层级、避免历史爆炸 |
| 3 | `EmitInternalEvents: true` | [08](08-multi-agent.md) | 让 deep_research 内部事件冒泡，前端能看进度不是黑盒 |
| 4 | DeepAgent 用 prebuilt，其他两个手写 ChatModelAgent | [08 §7](08-multi-agent.md) | deep_research 场景够复杂需要 middleware chain；其他简单场景轻量 ChatModelAgent 就够 |
| 5 | `WithoutGeneralSubAgent + WithoutWriteTodos` | [08](08-multi-agent.md) | 不允许 deep_research 套娃 + workspace 文件工具已够用 |
| 6 | 每个 agent 装 `approval + toolerr` middleware（顺序 approval 外 toolerr 内） | [05](05-tools-and-function-call.md) [08 §8](08-multi-agent.md) | approval 抛的 sentinel error 必须穿透，不能被 toolerr 吞掉 |
| 7 | Tool 全走 `utils.InferTool` 从结构体推 schema | [05](05-tools-and-function-call.md) | 类型安全 + schema 单一来源 |
| 8 | `get_current_time` description 强化"NEVER guess" | [05](05-tools-and-function-call.md) | 干预模型倾向直接靠常识回答的行为 |
| 9 | 所有 prompt 加"禁止叙述式假调用"负向约束 | [05](05-tools-and-function-call.md) | 防止本轮就撒谎"我查了" |
| 10 | Prompt 里"不要并发调用工具" 硬性纪律 | 用户新加 | ReAct 循环下并发 tool call 让 HITL 审批时序错乱，逐个串行更可控 |
| 11 | 消息结构化落库（raw 分行）+ handler 折叠展示 | [04](04-chatmodel-and-message.md) approval 修复 | 严格 tool_use/tool_result 配对给 LLM 回放，前端 API 契约不变 |
| 12 | `MessageRepo.AppendMany` 单事务 + `padMissingToolResults` | approval 修复 | 保证 turn 落库原子；cancel 中断补占位 tool row |
| 13 | `toSchemaMessages` 加 orphan-tool_call filter + warn log | approval 修复 | 最后一道防线：数据坏了也不 400 Claude |
| 14 | `checkpoint id = conversation id` | [08 §4](08-multi-agent.md) | 一 conv 一活跃 run 的模型下最简单可靠的 id 策略 |
| 15 | 默认内存 CheckPointStore（不用持久化 store） | [08 §4](08-multi-agent.md) | 与"进程内一次 run"语义天然吻合；重启不恢复是可接受损失 |
| 16 | RunCollector 双路径（老扁平 + 新分 turn） | [04 修复](.) | 扩展式加新方法，老逻辑不动，风险最小 |
| 17 | Extra dual-write（legacy tools + 新协议 rows 同时存） | approval 修复 | 前端老渲染路径不动，稳定后再删 Extra 老字段 |
| 18 | Workspace 读写权限不对称：写限 workspace 内 / 读放开绝对路径 | `prompts/general.go` | 现实需求：用户附件常在 `~/Downloads`；但写入必须受控 |

---

## 5. 面试可讲的四条故事线

### 5.1 故事线 A：架构迁移 —— 从 `flow/agent/react` 到 ADK

**背景**：Eino v0.5 之前只有 `flow/agent/react.NewAgent`，v0.5 加入 ADK。我们最初用 `NewReActAgent`（`internal/agent/agent.go` 还残留），后来遇到三个瓶颈：

1. 需要 sub-agent（deep_research / job_search），老路线只能自己把 sub-agent 包成 tool.BaseTool 手写
2. 前端要显示"agent 中间在做什么"，老路线只能靠 Callback 事后拿信息 + 单进程内自己攒
3. 想加 HITL 审批，老路线的 Interrupt/Resume 生态弱

**迁移动作**：把主入口从 `NewReActAgent` 换成 `NewInterviewADKAgent`（`internal/agent/adk_agent.go`），走 `adk.Runner + ChatModelAgent + AgentAsTool + AsyncIterator[AgentEvent]` 这套。老路线代码留着但主流程不用（`grep -rn NewReActAgent internal/` 只有定义处）。

**收益**：
- Sub-agent 通过 `NewAgentTool` 一行挂载
- `EmitInternalEvents=true` 让 sub-agent 事件冒泡到 root，UI 能挂父子 tool card
- HITL 落地成 `approval.Middleware` + `ResumeWithParams` 组合，代码量 ~100 行

**顺便被砍掉的代码**：`internal/stream/sse_handler.go::NewSSEHandler`（老路线的 Callback 实现），从此没调用点。

**面试问答**：
- 老路线还留着不删是有意的：作为架构演进的实物证据
- 迁移过程中最痛的地方：flow/agent/react 的 Runnable 返值和 ADK 的 AsyncIterator 语义完全不同，SSE 消费层 rewrite

### 5.2 故事线 B：HITL 审批从零到一

**需求**：写文件 / 执行 shell 命令 / 修改重要文件时要用户批准。

**难点**：LLM 是无状态的，不能靠 "prompt 里说要审批" 保证；必须框架层强制阻断。

**方案**：Eino v0.7+ 的 Interrupt & CheckPoint 协议。三层配合：

1. **中间件层**（`internal/approval/middleware.go`）：判断 `NeedsApproval(name, args)` → 抛 `tool.Interrupt(ctx, info)` sentinel error
2. **框架层**（Eino 内部）：识别 sentinel → 保存 checkpoint → 生成 `AgentEvent.Action.Interrupted` event → iter 自然终止
3. **应用层**（`internal/stream/adk_handler.go` + `internal/service/chat.go`）：`emitApprovalRequired` 发 SSE + `PendingStore.Record`；用户批准 → `chat.Resume` → `runner.ResumeWithParams(checkpointID, ResumeParams{Targets: {iid: decision}})` → 中间件里用 `tool.GetResumeContext` 拿 decision → `next(ctx, input)` 或 denial

**顺序坑**：`ToolCallMiddlewares: [approval, toolerr]` —— **approval 必须在外**，否则 `toolerr` 会把 Interrupt sentinel 当成普通错误包成"看似成功的 tool result"，interrupt 就被吃掉了。

**checkpoint id 策略**：直接用 `conversationID`（一个对话一次活跃 run，天然唯一）。

**详见** [08 §8](08-multi-agent.md) 完整时序图。

### 5.3 故事线 C："模型谎称调用了工具" 的排查与修复

**现象**：用户观察到模型经常说"我查了 X"、"我调用了 Y"，但**根本没发起 tool_call**。

**排查**：拆成两个独立机制：

1. **历史丢失**（A）：`toSchemaMessages` 只搬 3 字段（Role/Content/ReasoningContent），assistant 的 `ToolCalls` 结构和 Role=tool 的 tool_result 全丢；模型下一轮看不到"上轮真调过"的结构化证据，只能靠 Content 里的自然语言猜——一旦上一轮撒过一次谎，历史里就永远沉淀
2. **当轮撒谎**（B）：即使历史干净，模型也可能因为 Claude thinking 模式意图/执行错位 / prompt 缺硬约束 / tool description 模糊，跳过 tool_call 用叙述文字回答

**修复**（分 3 步）：

- **步骤 1**：prompt 加"禁止叙述式假调用"负向约束 + `get_current_time` description 加 "NEVER guess"（B 修）
- **步骤 2**：`RunCollector` 加 turn 结构；`adk_handler` wire `OpenTurn` / `AttachToolResult`（准备阶段，A 修基础）
- **步骤 3（一个 PR）**：DB 加 ToolName 字段 + `MessageRepo.AppendMany` 单事务；`runAgent::persistRun` 按 turn 落多行；`toSchemaMessages` 完整还原 ToolCalls + orphan filter；handler `foldMessages` 折叠展示（前端 API 契约不变）+ dual-write Extra.tools 保留老兼容

**Claude API 严格配对**是关键正确性约束：assistant 的每个 tool_use 必须紧跟对应 tool_result，否则 400。4 道防线：
1. ADK 事件流天然按 turn 排序
2. `AppendMany` 单事务
3. 中途 cancel 时 `padMissingToolResults` 补占位
4. 回放时 `toSchemaMessages` orphan filter

**收益**：新对话完整看到自己上轮 tool_call 结构，能准确说"我上轮调用了 A B C"；老对话 fallback 走 Extra.tools 展示、LLM 回放降级到"只有 Content"（不会 400 但会少见 tool history，可接受）。

### 5.4 故事线 D：Sub-Agent 事件冒泡 + 前端展示

**需求**：deep_research 是个后台研究员，可能跑几分钟。用户不能忍受"发出请求 → 5 分钟黑盒 → 一大坨结果"。

**方案**：
- **`EmitInternalEvents: true`**：`adk.ToolsConfig` 上开这个开关，sub-agent 的内部 assistant / tool 事件通过 root Runner 的 `AsyncIterator` 冒泡到 `ConsumeADKEvents`
- **`subAgentRouter`**（`internal/stream/adk_handler.go:154`）：维护 `sub-agent name → root tool_call_id` 的映射。当 supervisor 说"我要调 deep_research 工具，call_id=toolu_X" 时记下；此后所有 `AgentName="deep_research"` 的 event 发 SSE 时带 `parent_tool_call_id=toolu_X`
- **前端渲染**：把 sub-agent 事件挂在"supervisor 的 deep_research 工具卡片"下面展开，形成父子层级
- **落库**：sub-agent 事件不进主 assistant.Content（避免污染），单独落到 `Extra.sub_events` JSON 数组

**面试点**：跟"用 Callback 观测" 比，为什么这个方案更好？—— Callback 拿不到 `AgentName`，无法区分 root vs sub；AgentEvent 天然带这个字段。这是 06 章讲的 ADK 相对 Callback 的一个具体优势。

---

## 6. 项目里踩过 & 已解决的坑清单

| 坑 | 现象 | 修复 |
|---|---|---|
| 工具报错终止整轮 stream | tool 一次错误 → AgentEvent.Err 冒泡 → iter fatal → 用户看到断连 | `toolerr.Middleware` 把错误包成"看似成功的 tool result"让模型看到并重试 |
| 模型谎称调用工具 | 见故事线 C |  同上 |
| Claude 严格 tool_use ↔ tool_result 配对 | 半 turn 落库导致下轮 400 | AppendMany 单事务 + padMissingToolResults 补占位 + orphan filter |
| 前端刷新后一个 turn 显示两个 Interviewer | foldMessages 没合并同 turn 多个 assistant 行 | 加合并链：`lastAssistantIdx` + Content/Tools 追加 + id-based dedupe |
| 工具卡片重复显示 | 新协议的 ToolCalls placeholder + 老 Extra.tools 都注入了同一个 tool | id-based dedupe：合并到 prev.Tools 前用 `map[id]struct{}` 去重 |
| approval 顺序错乱 | 一个 turn 里模型并发调 3 个需审批的工具，UI 时序错乱 | Prompt 加"不要并发调用工具"硬性纪律，逐个串行 |

---

## 7. 项目还没做但值得做的

| 方向 | 位置 | 说明 |
|---|---|---|
| **接入 tracing** | callback 层 | 加 `AppendGlobalHandlers(langfuse.NewHandler(...))` 一行，即可拿到 token / latency / trace |
| **持久化 CheckPointStore** | RunnerConfig | 目前内存 store，进程重启丢中断态；换成 Redis-backing 即可跨重启恢复 |
| **RAG 全链路完善** | `internal/rag/retriever/` 已有 | 加 Indexer 侧的 batch 索引 job（当前只有 retriever 一侧）+ Document Loader 定期同步 |
| **TurnLoop 支持** | Runner | 让 agent 长连接跑，用户消息可以打断当前 turn（v0.9 能力，本项目还是"一 msg 一 Run"） |
| **模型 Failover** | ADK v0.9 有 ChatModel Failover 中间件 | Claude 挂了自动切 OpenAI，生产稳定性提升 |
| **Sub-agent 更丰富** | adk_agent.go | 目前 2 个 sub-agent；未来可能加 `interview_practice`（面试题演练）、`resume_analyzer` 等 |
| **Prompt 版本化** | prompts/ | 目前字符串常量硬编码；接入 A/B 测试要抽象成 ChatTemplate + 版本管理 |

---

## 8. 30 秒电梯陈述模板（面试自我介绍时用）

> 我做了一个 Go 语言的面试准备/生产力 Agent 应用，后端用 CloudWeGo eino v0.9 —— 一个字节开源的 Go LLM 应用框架，类似 LangChain 但强类型 + 流式一等公民。核心是它的 ADK 层（Agent Development Kit），我用 Supervisor + AgentAsTool 拓扑挂了两个专才 sub-agent：一个 DeepAgent 做复杂研究、一个 ChatModelAgent 做 Boss 直聘岗位抓取。做过一次从 flow/agent/react 老路线迁到 ADK 的架构演进；接入了 HITL 审批（借 Eino 的 Interrupt & CheckPoint 协议）；针对"模型谎称调用工具"这个 LLM 应用的经典问题做过一次完整的历史结构化回放修复 —— 从 DB schema 到 handler 折叠展示到前端渲染全链路走了一遍。最近在加 RAG（Milvus + OpenAI embedding）和 shell 命令工具。整个项目实践下来对 Go 泛型在框架里的应用、LLM 应用的观测机制、Multi-Agent 拓扑取舍这几个话题有比较深的体感。

---

## 9. 章节导航（快速回查）

| # | 文档 | 主题 | 面试重点 |
|---|---|---|---|
| 00 | [Overview](00-overview.md) | Eino 定位与设计哲学 | Eino vs LangChain 差异；两条路线；版本演进 |
| 01 | [Core Abstractions](01-core-abstractions.md) | Component / Runnable / Lambda / 类型 | Runnable 四方向 + 自动降级；`ToolCallingChatModel` 为什么并发安全 |
| 02 | [Orchestration](02-orchestration.md) | Chain / Graph / Workflow | Pregel vs DAG 引擎；4 条设计原则 |
| 03 | [Streaming](03-streaming.md) | StreamReader/Writer + 4 范式 | 读一次、Close 一次、扇出用 Copy；Concat / Boxing 两原语 |
| 04 | [ChatModel & Message](04-chatmodel-and-message.md) | 接口层次 + Message 全字段 | `ToolCalls.Arguments` 是 JSON 字符串；`ConcatMessages` |
| 05 | [Tools & Function Call](05-tools-and-function-call.md) | Tool 接口 + `utils.InferTool` + Middleware | function call 端到端环；toolerr middleware 的价值 |
| 06 | [Callbacks](06-callbacks.md) | Handler + 5 timing | Callback vs Middleware vs AgentEvent 三层切面 |
| 07 | [ReAct Agent](07-react-agent.md) | `flow/agent/react` 图结构 | Claude 的 StreamToolCallChecker 坑；老 vs 新路线对比 |
| 08 | [Multi-Agent](08-multi-agent.md) | ADK / Runner / AgentAsTool / HITL | HITL 完整时序图；4 种多 agent 拓扑；DeepAgent 内部 |
| 09 | [eino-ext 生态](09-eino-ext.md) | 三仓库 + components 生态 | 切 provider 的成本；RAG 三件套 |
| 10 | **本篇** | 项目落地整合 | 四条故事线 |
