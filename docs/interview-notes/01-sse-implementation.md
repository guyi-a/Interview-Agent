# SSE 实现：断连重连 + 页面刷新不重复渲染

> 面试可能会问到的技术点，都在这一篇里。所有代码引用都指向仓库真实位置。

## 面试一句话答法

我用 SSE 做 LLM 流式输出，核心设计是"**buffer 生命周期跟 HTTP 请求生命周期解耦**"：

- 每个 conversation 一个 `StreamBuffer`，存 agent 已经产出的所有 SSE 帧
- HTTP 请求只是这个 buffer 的一个"订阅者"，请求随便断随便重连，agent goroutine 一直在跑
- 页面刷新：先拉 DB 历史 → 探测"这个 conversation 有没有在跑"，**跑着就 attach，跑完了就 204**
- 前端拿到 200 SSE 才继续消费，拿到 204 就直接展示 DB 历史 —— 这就是**不重复渲染的关键**

---

## 需求 / 核心难点

LLM 一次回答走几十秒甚至几分钟，用户可能：

1. 刷新页面（tab reload）
2. 切走再切回来（React 组件 unmount / remount）
3. 网络抖动断连
4. 打开新 tab 进入同一个 conversation

要保证：

| 期望 | 反面 |
|---|---|
| 断了能重连接着看 | 断了就丢，回来只有半截 |
| 刷新后 UI 跟刷新前一致 | 刷新后重头再来一遍 |
| 已经写完的 assistant 消息不重复出现 | 页面上出现两遍相同回答 |
| 后端不因为断连而中止 agent | 用户一走 agent 就死 |

---

## 后端架构

三个 endpoint，都挂在 [`ChatHandler`](../../internal/handler/chat.go):

```
POST /chat/:id         → 发起新对话（或如果 streaming 就直接 attach）
GET  /chat/:id         → resume probe（页面刷新时前端首个调用）
POST /chat/:id/cancel  → 取消当前 agent
```

关键组件：

- `stream.StreamBuffer` [buffer.go:16](../../internal/stream/buffer.go#L16) —— 每个 conversation 一个
- `stream.Manager` [buffer.go:156](../../internal/stream/buffer.go#L156) —— `map[conversationID] *StreamBuffer`
- `service.ChatService` —— 起 agent goroutine，把生成的帧 Append 到 buffer

---

## StreamBuffer 的核心数据结构

```go
type StreamBuffer struct {
    mu          sync.RWMutex
    chunks      [][]byte       // 累积历史：所有 SSE 帧
    subscribers []chan []byte  // 广播新帧
    status      Status         // Streaming / Complete
    cancel      context.CancelFunc
}
```

两个作用叠在一起：

- **累积历史**：所有 append 过的 chunk 存下来，用来给新连上的客户端"回放"
- **实时广播**：每来一个 chunk，除了塞进 chunks，也塞进每个 subscriber 的 channel

`StreamAll(ctx)` [buffer.go:89](../../internal/stream/buffer.go#L89) 是核心：

```go
func (b *StreamBuffer) StreamAll(ctx context.Context) <-chan []byte {
    out := make(chan []byte, 16)
    
    b.mu.Lock()
    history := make([][]byte, len(b.chunks))
    copy(history, b.chunks)         // 加锁快照历史
    if b.status == StatusComplete {
        b.mu.Unlock()
        go func() {                  // 已完成：只回放，不订阅
            defer close(out)
            for _, c := range history {
                select { case <-ctx.Done(): return; case out <- c: }
            }
        }()
        return out
    }
    
    sub := make(chan []byte, 64)
    b.subscribers = append(b.subscribers, sub)  // 未完成：先订阅再放锁
    b.mu.Unlock()
    
    go func() {
        defer close(out)
        defer b.unsubscribe(sub)
        // 阶段 1：回放历史
        for _, c := range history { ...out <- c... }
        // 阶段 2：跟着 sub tail 新帧
        for { case c := <-sub: out <- c ... }
    }()
    return out
}
```

**关键设计**：**"快照历史" 和 "注册订阅"是在同一把锁下面**，保证接下来 Append 进来的帧一定会通过 subscriber 送到，不会漏帧也不会有跟历史重复的帧。

---

## subscriber 到底是啥

字面上就是**"当前正连着这个 buffer 的一个 HTTP 请求"**。数据结构上是 `chan []byte`，一个订阅者一个 channel。

具体流转（[buffer.go:110-116](../../internal/stream/buffer.go#L110)）：

- HTTP handler 调 `buf.StreamAll(ctx)` → buffer 加锁里创建一个新 channel `sub := make(chan []byte, 64)` → append 到 `subscribers` 数组
- Agent 每产生一帧 → `buf.Append(chunk)` 遍历所有 subscriber → 把 chunk **广播**到每个 channel（`select-default` 满了就丢，见"边界"章节）
- HTTP handler 的 goroutine 从这个 sub channel 读 → 写 HTTP response body → `flusher.Flush()`
- HTTP handler 结束（客户端断连 / stream Finish）→ `defer b.unsubscribe(sub)` 把 channel 从 subscribers 数组里摘掉

**广播模式**：一个 buffer 可以有多个 subscriber，agent 是电视台，buffer 是信号中枢。中枢除了实时广播（subscribers），还录像（chunks），后到的订户能"回看"再"跟播"。

**多订阅者实例**：
- Tab A 触发 POST → POST handler 是 subscriber #1
- Tab B 打开同 conversation → GET handler 是 subscriber #2
- 两边独立 channel、同时收到实时帧，谁断谁的 unsubscribe，互不影响

---

## POST：发起新对话（或 attach 已 streaming）

[chat.go:31-51](../../internal/handler/chat.go#L31)：

```go
func (h *ChatHandler) Chat(c *gin.Context) {
    id := c.Param("id")
    
    // 幂等：同一个 conversation 已经在跑就直接 attach，不重复起 agent
    if h.chat.IsStreaming(id) {
        writeSSE(c, h.chat.Get(id))
        return
    }
    
    // 否则起新一轮 agent
    buf, err := h.chat.Start(ctx, id, req.Message, projectID)
    writeSSE(c, buf)
}
```

`Start` 里做的事 [service/chat.go:80](../../internal/service/chat.go#L80)：

1. 存用户消息到 DB
2. `manager.Create(id)` 建新 buffer
3. **起 `go s.runAgent(runCtx, ...)`**（这一步是关键 —— 后台 goroutine 独立于 HTTP 请求生命周期）
4. 返回 buffer 给 handler 消费

`writeSSE` 只干一件事：设置 SSE headers，然后从 `buf.StreamAll(c.Request.Context())` 拿 chan 循环写：

```go
ch := buf.StreamAll(c.Request.Context())
for frame := range ch {
    c.Writer.Write(frame)
    flusher.Flush()
}
```

**c.Request.Context() 是绑到 HTTP 请求的**，客户端断连时 context 会 Done，`StreamAll` 的 goroutine 检测到就退出、unsubscribe。**但 buffer 本身和 agent goroutine 都不受影响**。

---

## GET：resume probe（页面刷新的核心）

[chat.go:53-64](../../internal/handler/chat.go#L53)：

```go
func (h *ChatHandler) Resume(c *gin.Context) {
    id := c.Param("id")
    if !h.chat.IsStreaming(id) {
        c.Status(http.StatusNoContent)   // 204：没在跑，你别 attach
        return
    }
    writeSSE(c, h.chat.Get(id))
}
```

**为什么这里不 attach 已完成的 buffer？** 注释写得很清楚：

> Only resume in-flight streams. Completed buffers stay in the manager so the original POST client can drain its `done`/`error` frame, but a reload client should read history from the DB instead — replaying a completed buffer would duplicate the persisted assistant message.

**这就是"页面渲染不重复"的核心逻辑**：

- Buffer 完成后（`Finish()` 被调），status 变成 `Complete`
- 但 buffer 仍在 `manager` 里 —— 原 POST 客户端如果还没消费完，还能拿到最后的 `done` 帧
- **新的 GET 请求**（reload 触发）判定"不在跑" → 204
- 前端拿到 204 → 不 attach → 只走 DB 历史 → 不重复

---

## 追问：DB 里已经有完整消息了，回放会不会重复渲染？

**主线场景：不会**。因为"回放路径根本不激活" —— DB 有完整消息 ⟺ agent 已经跑完 ⟺ status=Complete ⟺ GET 返 204 ⟺ 前端根本不 attach SSE，没连上就没有回放。

**但代码里存在一个几十毫秒的极端竞态窗口**，值得知道（面试官会追问就诚实答）。

看 [`consumeAndPersist`](../../internal/service/chat.go#L357) 的执行顺序：

```go
1. ConsumeADKEvents(...)     // 消费 iter，往 buf 里 Append 帧
2. persistRun(collector)     // 写 DB     ← DB 已经完整
3. FinalizeOK(buf)           // append done 帧 + buf.Finish()  ← status 才翻 Complete
```

**步骤 2 完了、步骤 3 还没开始的那几十毫秒**：

- DB 里 assistant 消息 **已经完整**（有 tools、有 content、tools 状态都是 ok/error）
- 但 `buf.status` 还是 `Streaming`
- 用户这瞬间 F5 → GET 判定 `IsStreaming == true` → 返 200 SSE 流
- 前端 attach → 回放全部历史帧 + done 帧
- **`initialTurns` 里最后一个 assistant turn 是完整的**（DB 恢复出来的），[useChatStream.ts:634](../../web/src/hooks/useChatStream.ts#L634) 查找"有 pending/running tool 的 turn" → **找不到**（tools 全是 ok/error）
- 走 else 分支 → **新建 resume turn**
- 回放的帧都塞到这个新 turn 里 → **UI 上出现两个 assistant turn**

**触发条件很严格**（实操几乎撞不到）：
1. F5 必须落在 DB 写完 → buf.Finish 这几十 ms 之间
2. 网络够快让 SSE 请求真的连上（否则 status 已经 Complete）

**三种改法**（当前代码是"不处理"）：

- **A. Finish 提前**：先 `buf.Finish()` 再 `persistRun`，先翻状态再写 DB → 但原 POST 客户端可能拿不到 done 帧就断
- **B. GET 加判断**：Complete 状态且历史最后一帧是 done → 也返 204 → 需要 buffer 加"peek 最后一帧"
- **C. 前端加去重（最小侵入）**：resume 拿到 200 后先检查最后一个 assistant turn 是否 `done: true`；是的话直接 `res.body.cancel()` 不 attach

**面试怎么答**：直接说主线不会重复（GET 返 204 挡住了），如果被追问"极端时序呢"，坦诚说存在窗口 + 讲改法 C。这比"完美无缺"更有说服力。

---

## 前端 mount 时的三步走

[useChatStream.ts:569](../../web/src/hooks/useChatStream.ts#L569) 里的 useEffect：

```
1. listMessages(convID)          → DB 拉历史 → fromPersisted() 生成 turns (done:true)
2. listPendingApprovals(convID)  → 恢复未决审批 UI
3. resumeChat(convID)            → GET /chat/:id 探测
   - 204：没在跑，只有历史，直接结束 mount
   - 200 SSE：有 in-flight，attach 上去继续
```

**关键：DB 历史和 SSE 连接是两条独立数据源**，先跑历史，再看要不要 attach。因为后端 GET 保证只在真的还在跑时才返 SSE 流，所以：

- 后端还在跑 → 历史里最新一条 assistant turn 是**半截**（done:false / 有 pending tool）→ 前端 attach 到这条 turn 继续
- 后端跑完了 → 历史里最新一条 assistant turn **是完整的**（done:true）→ 前端不 attach，就展示历史

**Attach 时找哪个 turn**：[useChatStream.ts:634](../../web/src/hooks/useChatStream.ts#L634)

```typescript
const existingTurn = [...initialTurns]
  .reverse()
  .find(
    (t) => t.role === "assistant" &&
      t.tools.some((tool) => tool.status === "pending" || tool.status === "running"),
  );
```

找**最后一个还有 pending/running tool 的 assistant turn**，复用它的 id，把新帧 update 上去，而不是新建一个 turn。这样：

- Tool card 的 id 跟 SSE 帧的 `f.id` 对得上 → `upsertTool` 只 update 不新增
- 不会出现"半截 turn + 空 resume turn"两个都渲染

---

## 帧的 id 匹配（另一层去重保障）

每个 tool_call SSE 帧带 `id`（后端 [sse_handler.go:415](../../internal/stream/sse_handler.go#L415) 里 `atomic.AddInt64(&toolCounter, 1)` 生成 `tc-1 / tc-2 / ...`）。

前端 `upsertTool(id, patch)` 用这个 id 定位 tool card：
- id 已存在 → **合并** patch（比如把 `tool_call` 的 name / args 跟后来的 `tool_result` 的 status / content 合起来）
- id 不存在 → 新建

即使同一份帧被回放两次（理论上有可能，比如手工 Ctrl+R 快速刷新前后消费到了同一帧），upsertTool 也只会重复覆盖同一个 tool card，不会产生"两份 tool card"。

---

## 断连怎么不影响 agent

关键设计：**agent goroutine 用的 context 是独立的**，跟 HTTP 请求的 context 完全没关系。

[service/chat.go:122](../../internal/service/chat.go#L122)：

```go
buf := s.manager.Create(id)
runCtx, cancel := context.WithCancel(context.Background())   // 不用 request 的 ctx
buf.SetCancel(cancel)
go s.runAgent(runCtx, id, history, buf)
```

`runCtx` 从 `context.Background()` 起，**不继承** HTTP 请求的 context。所以：
- 客户端断开 → 只影响 `StreamAll` 的转发 goroutine 退出、unsubscribe，agent 继续跑
- 只有明确调 `POST /chat/:id/cancel` 触发 `buf.Cancel()` 才会 cancel runCtx，agent 停

---

## Cancel 的行为

[buffer.go:34](../../internal/stream/buffer.go#L34)：

```go
func (b *StreamBuffer) Cancel() bool {
    c := b.cancel
    if c == nil { return false }
    c()               // cancel runCtx → agent goroutine 收到 → 提前退出
    return true
}
```

用户点"停止"→ 前端 `cancelChat()` 发 POST → 后端调 `buf.Cancel()` → agent 退出 → 发一个 `done` / `error` 帧 → buf.Finish() → subscriber 收到 close → 前端 SSE 循环退出。

**注意 buf 本身不会被删**，还留在 manager 里，让接下来的 reload 走 GET 时能正确返 204。

---

## Buffer 什么时候真的清掉？

**只在进程退出时**，`Manager.ShutdownAll()` [buffer.go:194](../../internal/stream/buffer.go#L194) 遍历所有 buffer 调 Cancel + Finish。

**故意不做 TTL 清理**，原因：
- Completed buffer 占内存不大（几十 KB 到几 MB 的帧数据）
- 保留就能让"多客户端并发看同一 conversation"正常工作（比如 A tab 触发对话，B tab 打开同 conversation → GET → 拿到 in-flight buf → attach）
- 进程重启就自动清，不需要额外的清理逻辑

---

## SSE Frame 的编码格式

[sse_handler.go:384](../../internal/stream/sse_handler.go#L384)：

```go
func Encode(f Frame) []byte {
    data, _ := json.Marshal(f)
    out := make([]byte, 0, len(data)+8)
    out = append(out, []byte("data: ")...)
    out = append(out, data...)
    out = append(out, '\n', '\n')
    return out
}
```

标准 SSE 格式：`data: {json}\n\n`。**没有用 `event:` 命名类型**，type 塞在 JSON payload 里（Frame.Type）—— 好处是所有帧一个统一的 shape，前端一个 `parseFrames` 全搞定，坏处是丢了 SSE 原生 event routing 能力，不过我们用不上。

前端解析 [useChatStream.ts:116](../../web/src/hooks/useChatStream.ts#L116)：

```typescript
function parseFrames(buffer: string): { frames: Frame[]; rest: string } {
  const frames: Frame[] = [];
  let rest = buffer;
  while (true) {
    const idx = rest.indexOf("\n\n");    // SSE 帧分隔
    if (idx < 0) break;
    const block = rest.slice(0, idx);
    rest = rest.slice(idx + 2);
    const dataLines: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("data:")) dataLines.push(line.slice(5).trimStart());
    }
    if (dataLines.length === 0) continue;
    frames.push(JSON.parse(dataLines.join("\n")) as Frame);
  }
  return { frames, rest };
}
```

**不用浏览器原生的 EventSource**，用 `fetch + ReadableStream + TextDecoder`。原因：
1. EventSource 不支持 POST（我们 POST 触发对话）
2. EventSource 断了会自动重连，但我们需要精确控制重连时机（前端 pull-based 3 步走）
3. EventSource 不能带 AbortController 精确 cancel

---

## 边界 case

| 场景 | 处理 |
|---|---|
| 客户端断连 | `c.Request.Context()` Done → 转发 goroutine 退出 unsubscribe，agent 不影响 |
| 用户快速 reload | 老 mount `cancelled = true` + `controller.abort()`，新 mount 走三步走 |
| 一个 conversation 两个 tab 同时打开 | 两边 GET 都拿到同一个 buf 的 SSE 流，各自独立 subscriber，都会收到实时帧 |
| 用户 POST 时已经在 stream | `IsStreaming` 检查生效 → 直接 attach 不重复起 agent |
| Agent 报错 | `runAgent` 里 write `error` frame → buf.Finish() → 前端拿到 `error` 走错误分支 |
| 进程重启 | 所有 in-flight buffer 丢失 —— 前端刷新时 GET 返 204 → 走 DB 历史（DB 里没写完的 assistant 消息会是半截，但至少不 crash） |

---

## 面试官可能追问的点

**Q：为什么不用 WebSocket？**

A：SSE 单向就够了（服务器 → 客户端流），HTTP/1.1 上跑，代理友好；WebSocket 是双向的但要额外握手、心跳、断线重连都要自己写。我们 client → server 就用普通 POST，不需要长连接双向。

**Q：如果一直不断连，buffer 越积越大怎么办？**

A：单次 conversation 的 buffer 上限约束在 agent 本身（LLM 一次回答几十 KB 到几 MB）。跨 conversation 有 `manager` 存，进程重启清；如果长期运行想加 TTL 淘汰，加一个 goroutine 定期扫 `status == Complete && idle > 1h` 的 buffer 删掉就行。当前设计故意省了这个（够简单）。

**Q：subscriber 的 channel 缓冲 64 满了会怎么样？**

A：[buffer.go:60](../../internal/stream/buffer.go#L60) 里 `select { case ch <- cp: default: }` —— **满了就丢**。牺牲丢帧换 producer 不阻塞。前端 fetch stream 消费速度一般远快于 LLM 产出，实际不会满。如果真出现丢帧，前端下次刷新时走 DB 历史能兜底（DB 是权威源）。

**Q：如果两个 POST 同时打过来同一个 conversation ID？**

A：`Chat` handler 里 `IsStreaming(id)` 是加锁读的 —— 第一个 POST 进入 `Start()` 建 buffer，第二个 POST 进来时看到 `IsStreaming == true` → 直接 attach。**竞态窗口只有 `IsStreaming` 返回 false 到 `manager.Create` 之间**，实际这段短到不会真发生，但要严谨的话可以给 `Start()` 加 per-id 锁。

**Q：为什么用 `context.Background()` 派生 runCtx 而不是复用 request 的？**

A：这是"断连不影响 agent"最核心的一步。用 request 的 context 派生 → 请求断 → runCtx cancel → agent 停 → 用户刷新回来发现啥都没了。用 `Background()` → agent 完全独立，只有明确的 `Cancel()` 才能停。

**Q：SSE 帧里为什么每条都带 conversation-level id 而不是全局递增？**

A：Tool call id 是 per-run 的 `tc-N`（`atomic.AddInt64` 每个 handler 一份 counter），足够在同一次 stream 内做 tool_call ↔ tool_result 匹配。跨 conversation 不需要唯一，因为帧是按 conversation 分流的（每个 conversation 独立 buffer）。

**Q：done 帧之后 buffer 还留着，会不会内存泄漏？**

A：会累积，但 completed buffer 只占那次 run 累积的帧字节数（几十 KB ~ 几 MB），一个用户日常几十个 conversation 也不到 GB 级。真上生产要加 TTL 清理（"完成后 N 小时没人订阅就删"）。当前个人项目省了。

---

## 一句话小结

**buffer 是权威、DB 是终态、请求是订阅者**：这三个角色分清楚，就能理解为啥断连能重连、刷新不重复。
