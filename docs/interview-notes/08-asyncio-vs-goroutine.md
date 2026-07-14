# 08 · Python asyncio vs Go goroutine —— 心智映射与面试实战

**场景**：面试官出 Go 并发题（三协程轮流打印、生产者消费者、限流），你只想用 Python asyncio 答。本文给出一一对应的心智映射 + 常见题型的 asyncio 解法 + 高频坑。

**前置立场**：**asyncio 是 Python 里最贴近 Go goroutine 的抽象**（协程 + Queue 传消息 + gather 汇合），比 `threading` 更接近。这是本文所有比较的默认前提。

---

## 1. 一句话对应表

| Go 概念 | Python asyncio 对应 | 备注 |
|---|---|---|
| `goroutine`（`go f()`） | 协程 (`async def` + `await`) | Go 的 goroutine 是内核之上的 M:N 调度；asyncio 是**单线程事件循环**上的协作式协程 |
| `runtime.GOMAXPROCS` | 单线程（asyncio）；CPU 密集换 `multiprocessing` | asyncio 不利用多核 |
| `chan T`（无缓冲） | `asyncio.Queue(maxsize=1)` + 严格 put/get 配对；或 `asyncio.Event` | 无缓冲 channel 的"发送阻塞直到有人接收"，Queue(1) 只近似（发送方 put 完就返回，只要队列没满） |
| `chan T`（有缓冲） | `asyncio.Queue(maxsize=N)` | 直接对应 |
| `<-ch`（接收） | `await q.get()` | 阻塞取一个值 |
| `ch <- v`（发送） | `await q.put(v)` / `q.put_nowait(v)` | put_nowait 满就抛 QueueFull |
| `close(ch)` | 约定发一个哨兵值（`None`）；或用 `asyncio.Event` 标记结束 | asyncio.Queue **没有 close 语义** |
| `for v := range ch` | `while (v := await q.get()) is not None:` | 通常配合哨兵 |
| `select { case ...: }` | `asyncio.wait([...], return_when=FIRST_COMPLETED)` | 更麻烦，见 §7 |
| `sync.WaitGroup` | `asyncio.gather(*tasks)` / `asyncio.TaskGroup`（3.11+） | TaskGroup 是推荐版，异常处理更好 |
| `sync.Mutex` | `asyncio.Lock()` | 但**单线程 asyncio 里 90% 场景根本不需要锁**（没有抢占） |
| `context.Context` + `cancel()` | `asyncio.Task.cancel()` + `asyncio.CancelledError` | 取消是异常传播，不是标志位 |
| `context.WithTimeout` | `asyncio.wait_for(coro, timeout=T)` / `async with asyncio.timeout(T):`（3.11+） | 超时会 raise TimeoutError |
| `time.Sleep(t)` | `await asyncio.sleep(t)` | **千万别用 `time.sleep`**，见 §8 |
| `runtime.Gosched()` | `await asyncio.sleep(0)` | 主动让出事件循环 |
| `pprof` goroutine dump | `asyncio.all_tasks()` + `Task.get_stack()` | 观测手段弱一些 |

---

## 2. 心智锚点：为什么 asyncio 比 threading 更像 goroutine

Python 有三条并发路线：

| 路线 | 并行度 | 适合 | 跟 Go 谁像 |
|---|---|---|---|
| `threading` | GIL 限制 → **一次一个线程跑 Python bytecode** | I/O 密集（阻塞系统调用会释放 GIL） | 像 Go 但语义错位（阻塞 API、抢占式调度） |
| `asyncio` | 单线程事件循环，协作式 | I/O 密集（大量并发连接） | **最像 goroutine + channel** |
| `multiprocessing` | 真并行 | CPU 密集 | 像 Go + `GOMAXPROCS > 1`，但重得多 |

**关键差异**（asyncio vs goroutine，别忽略）：

1. **调度**：Go 抢占式（v1.14+），任何函数都可能被切出；asyncio **协作式**，只有 `await` 处才让出 —— 一段没 await 的计算会**独占事件循环**，别的协程全饿死
2. **并行**：Go 在多核上真并行；asyncio 永远只有一个线程在跑 Python 代码
3. **channel**：Go channel 是语言原生 + `close()` 一等公民；asyncio 只有 Queue，**没有 close**，要靠哨兵值或 Event 手动传"结束"

---

## 3. 经典题 A：三协程轮流打印 cat/dog/fish

**Go 原题**：三个 goroutine，每秒打印一次 cat/dog/fish，顺序固定，协程 1 打印 cat，2 打印 dog，3 打印 fish。

**Go 参考解**（对照用）：

```go
func main() {
    var wg sync.WaitGroup
    wg.Add(3)
    chcat, chdog, chfish := make(chan struct{}), make(chan struct{}), make(chan struct{})
    go printCat(&wg, chcat, chdog)
    go printDog(&wg, chdog, chfish)
    go printFish(&wg, chfish, chcat)
    chcat <- struct{}{}  // 启动
    wg.Wait()
}
```

三个 channel 组成**环**：cat 打完 → 通知 dog → dog 打完 → 通知 fish → fish 打完 sleep 1s → 通知 cat。

**Python asyncio 版**（直译）：

```python
import asyncio

async def print_cat(cat_q, dog_q):
    while True:
        await cat_q.get()
        print("cat")
        dog_q.put_nowait(None)

async def print_dog(dog_q, fish_q):
    while True:
        await dog_q.get()
        print("dog")
        fish_q.put_nowait(None)

async def print_fish(fish_q, cat_q):
    while True:
        await fish_q.get()
        print("fish")
        await asyncio.sleep(1)
        cat_q.put_nowait(None)

async def main():
    cat_q, dog_q, fish_q = asyncio.Queue(), asyncio.Queue(), asyncio.Queue()
    cat_q.put_nowait(None)                # 环上塞第一个信号
    await asyncio.gather(
        print_cat(cat_q, dog_q),
        print_dog(dog_q, fish_q),
        print_fish(fish_q, cat_q),
    )

asyncio.run(main())
```

**逐行对应**：

| Go | Python |
|---|---|
| `go printCat(...)` | `asyncio.gather(print_cat(...), ...)` |
| `chcat := make(chan struct{})` | `cat_q = asyncio.Queue()` |
| `<-cat` | `await cat_q.get()` |
| `dog <- struct{}{}` | `dog_q.put_nowait(None)` |
| `chcat <- struct{}{}` (main 里启动) | `cat_q.put_nowait(None)` (main 里启动) |
| `wg.Wait()` | `await asyncio.gather(...)`（gather 本身就 wait） |

**为什么用 `put_nowait` 不用 `await q.put(None)`**：Queue 默认 `maxsize=0` 即无界，`put` 永不阻塞，两者等价；写 `put_nowait` 更明确"我在发信号，不会阻塞"。如果 Queue 有 maxsize 就要 `await put()`。

---

## 4. 经典题 B：生产者消费者

**Go 版**：

```go
ch := make(chan int, 10)
var wg sync.WaitGroup

// 3 个生产者
for i := 0; i < 3; i++ {
    wg.Add(1)
    go func(id int) {
        defer wg.Done()
        for j := 0; j < 5; j++ {
            ch <- id*100 + j
        }
    }(i)
}

// 关闭 channel
go func() { wg.Wait(); close(ch) }()

// 消费者：range 到 channel 关闭
for v := range ch {
    fmt.Println(v)
}
```

**Python asyncio 版**：

```python
import asyncio

async def producer(q, pid):
    for j in range(5):
        await q.put(pid * 100 + j)

async def consumer(q):
    while True:
        v = await q.get()
        if v is None:                    # 哨兵表示结束
            break
        print(v)

async def main():
    q = asyncio.Queue(maxsize=10)
    producers = [asyncio.create_task(producer(q, i)) for i in range(3)]
    consumer_task = asyncio.create_task(consumer(q))
    await asyncio.gather(*producers)     # 等所有生产者跑完
    await q.put(None)                    # 通知消费者结束
    await consumer_task

asyncio.run(main())
```

**关键差**：
- **Go 有 `close(ch)`** —— consumer 的 `for range` 自动结束。asyncio Queue **没有 close**，只能靠**哨兵值**（`None`）或额外的 `asyncio.Event` 通知
- **多消费者结束**：如果有 N 个消费者，得 put N 个 None。或用 `asyncio.Event().set()` 让每个消费者在 `await event.wait()` 结束时退出

**改进：用 asyncio.TaskGroup (Python 3.11+)**：

```python
async def main():
    q = asyncio.Queue(maxsize=10)
    async with asyncio.TaskGroup() as tg:
        for i in range(3):
            tg.create_task(producer(q, i))
        tg.create_task(consumer(q))
        # TaskGroup 结束前所有 task 必须完成 —— 但 consumer 是 while True，得改成条件退出
```

TaskGroup 语义更严，但对"消费者是无限循环"的场景不友好；哨兵值 + `gather` 更实用。

---

## 5. 经典题 C：交替打印数字和字母（1A2B3C…）

**Go 版**：

```go
chNum, chLet := make(chan struct{}, 1), make(chan struct{})

go func() {
    for i := 1; i <= 26; i++ {
        <-chNum
        fmt.Print(i)
        chLet <- struct{}{}
    }
}()
go func() {
    for i := 'A'; i <= 'Z'; i++ {
        <-chLet
        fmt.Printf("%c", i)
        chNum <- struct{}{}
    }
}()

chNum <- struct{}{}   // 启动
time.Sleep(time.Second)
```

**Python asyncio**：

```python
async def print_num(num_q, let_q):
    for i in range(1, 27):
        await num_q.get()
        print(i, end="")
        let_q.put_nowait(None)

async def print_let(let_q, num_q):
    for c in map(chr, range(ord("A"), ord("Z") + 1)):
        await let_q.get()
        print(c, end="")
        num_q.put_nowait(None)

async def main():
    num_q, let_q = asyncio.Queue(), asyncio.Queue()
    num_q.put_nowait(None)
    await asyncio.gather(print_num(num_q, let_q), print_let(let_q, num_q))
    print()

asyncio.run(main())
```

**思路完全一致**：两个协程握手对拍，一个先动。

---

## 6. 经典题 D：限流 / 信号量

**Go 版**（用 buffered channel 当信号量，最多 3 个并发）：

```go
sem := make(chan struct{}, 3)
for _, url := range urls {
    sem <- struct{}{}
    go func(u string) {
        defer func() { <-sem }()
        fetch(u)
    }(url)
}
```

**Python asyncio**（用 `asyncio.Semaphore`）：

```python
sem = asyncio.Semaphore(3)

async def fetch_limited(url):
    async with sem:
        await fetch(url)

await asyncio.gather(*[fetch_limited(u) for u in urls])
```

Python 这里比 Go 更**语义化** —— `async with sem` 直接表达"进入临界区"，比 Go 手写 `sem <- {}` / `<-sem` 显式。**面试点**：这是 Python 少数比 Go 表达力更强的场景之一。

---

## 7. `select` 的 asyncio 对应 —— 最痛的一处

Go 的 `select` 是**多路复用 channel**：

```go
select {
case v := <-ch1:
    handle1(v)
case ch2 <- x:
    handle2()
case <-time.After(1 * time.Second):
    timeout()
}
```

Python asyncio 没有直接对应，得用 `asyncio.wait(..., return_when=FIRST_COMPLETED)` 拼：

```python
async def multi_recv(q1, q2):
    t1 = asyncio.create_task(q1.get())
    t2 = asyncio.create_task(q2.get())
    timeout = asyncio.create_task(asyncio.sleep(1))
    done, pending = await asyncio.wait(
        {t1, t2, timeout},
        return_when=asyncio.FIRST_COMPLETED,
    )
    for t in pending:
        t.cancel()  # 清理未完成的（重要！否则泄漏）
    if t1 in done: handle1(t1.result())
    elif t2 in done: handle2(t2.result())
    else: timeout_handler()
```

**痛点**：
- 每次都要**手动创建 Task + 手动清理未完成的 Task**，否则 `q.get()` 挂着一直没人取
- Go 的 `select` 可以同时监视**发送** (`ch2 <- x`) 和接收，Python 表达发送侧的多路复用更麻烦（要把 `q.put(x)` 也包成 task）
- 超时 case 得手动加 `asyncio.sleep(T)` 作为一个 task

**替代方案**：**用 asyncio.timeout()（3.11+）表达超时**，比塞进 wait 简洁：

```python
try:
    async with asyncio.timeout(1):
        v = await q.get()
except asyncio.TimeoutError:
    timeout_handler()
```

**面试点**：如果被问"Go 的 select 在 Python 里怎么写"，直接说"asyncio.wait + FIRST_COMPLETED，但没 Go 那么优雅，超时单独用 asyncio.timeout"。

---

## 8. 高频坑（面试爱问 & 生产事故爱翻）

### 8.1 千万别用 `time.sleep()`

```python
async def bad():
    time.sleep(1)   # ❌ 阻塞整个事件循环，其他协程全饿死
```

正确：

```python
async def good():
    await asyncio.sleep(1)   # ✓ 让出事件循环
```

**同理别用**：`requests.get()`（用 `httpx.AsyncClient` 或 `aiohttp`）、`open().read()`（大文件用 `aiofiles`）、`subprocess.run()`（用 `asyncio.create_subprocess_exec`）。**任何阻塞系统调用都会卡死事件循环**。

### 8.2 CPU 密集不适合 asyncio

asyncio 是**单线程**，一段 pure Python 计算跑起来其他协程全等着。CPU 密集用：

- `concurrent.futures.ProcessPoolExecutor` + `loop.run_in_executor()`
- 或者干脆用 `multiprocessing`

Go 天然多核，这个坑不存在。

### 8.3 忘了 `await`

```python
async def foo():
    ...

# ❌ 什么也没发生（返回一个 coroutine 对象，从没被跑）
foo()

# ✓
await foo()
# 或者
asyncio.create_task(foo())
```

**Python 3.11+ 会给 warning**："coroutine 'foo' was never awaited"。面试时提到这个 warning = 加分。

### 8.4 `create_task` 的引用陷阱

```python
async def leaky():
    for i in range(1000):
        asyncio.create_task(worker(i))   # ⚠️ task 没保存引用
```

**Python 官方文档**：`create_task` 返回的 Task 必须**保留强引用**，否则 GC 可能在它跑完前把它回收。正确：

```python
tasks = []
for i in range(1000):
    tasks.append(asyncio.create_task(worker(i)))
```

或者用 `asyncio.gather`/`TaskGroup` 自动管理。

### 8.5 取消不是"停止"，是"抛异常"

```python
task = asyncio.create_task(work())
task.cancel()      # 触发 CancelledError 在 task 里 raise
try:
    await task
except asyncio.CancelledError:
    pass
```

**别在协程里 `except Exception: pass`** —— 会把 `CancelledError` 也吃掉，task 不响应取消。要 `except Exception` 时**必须 `except (asyncio.CancelledError,)` 优先 re-raise**。Python 3.8+ 里 `CancelledError` 已经继承 `BaseException` 而不是 `Exception`，`except Exception` 不会捕获它 —— 但很多老代码有这个坑。

**Go 的对应**：`ctx.Done()` 是 select case，需要主动检查；不像 asyncio 靠异常自动传播。

### 8.6 asyncio.Queue 没有 close

Go：`close(ch)` → 消费者 `for range` 自动退出
Python：**没有等价物**。三个方案：
1. 发哨兵 `None`（多消费者要发 N 个）
2. 用 `asyncio.Event().set()` 表示结束
3. 自己封装一个 `ClosableQueue` 类

---

## 9. asyncio 相对 Go 的**优势**（面试反问装到）

不是所有点都是 Go 完胜。asyncio 在**几处**其实更好：

1. **Semaphore/Lock 更语义化**：`async with sem:` / `async with lock:` 比 Go 的 `sem <- {}` / `<-sem` 或 `mu.Lock() / defer mu.Unlock()` 更贴近"作用域"
2. **超时/取消是异常传播**：`try/except` 天然聚集处理点，不用像 Go 那样在每个函数签名里带 `ctx`
3. **`async for` / `async with`**：迭代器和上下文管理器都能异步，Go 需要手写
4. **`asyncio.gather(return_exceptions=True)`**：优雅收集所有 task 的异常，Go 得自己 wg + err channel

---

## 10. 如果面试官坚持"用 Python 但要多线程"

Python 里的 `threading` **在 I/O 密集场景一样能跑并发**（阻塞系统调用会释放 GIL），只是心智离 Go 更远。核心映射：

| Go | Python threading |
|---|---|
| `goroutine` | `threading.Thread(target=..., daemon=True)` |
| `chan` | `queue.Queue()`（**线程安全**） |
| `<-ch` / `ch <-` | `q.get()` / `q.put()`（阻塞） |
| `sync.WaitGroup` | `Thread.join()` × N；或 `threading.Barrier(N)` |
| `sync.Mutex` | `threading.Lock()` |
| `context` cancel | 自己传 `threading.Event()` 检查 |

**要点**：面试官若问"为什么不用 threading"，答"asyncio 单线程无锁语义更贴近 Go 协程；threading 要处理 GIL + 抢占，跟 Go 心智错位（虽然也能干活）"。CPU 密集问题两条路都不行，切 `multiprocessing`。

---

## 11. 一分钟口头模板（面试官问"Python 怎么写这道 Go 题"）

> **"我用 asyncio。goroutine 对应 async 协程，channel 对应 asyncio.Queue，WaitGroup 对应 asyncio.gather。三个协程组成环，每个协程 `await` 上游 Queue 拿信号 → 打印 → 往下游 Queue put 一个信号；main 里初始 put 一个启动，然后 `asyncio.gather` 等所有协程。**
>
> **区别是：Python asyncio 是单线程协作式调度，没有抢占；Queue 没有 close 语义，要发哨兵值或用 Event 通知结束；`select` 语法不如 Go 优雅，要用 `asyncio.wait(FIRST_COMPLETED)` 加手动清理未完成的 task。**
>
> **CPU 密集不适合 asyncio，那种场景我用 multiprocessing；I/O 密集 asyncio 是最接近 Go 心智的方案。"**

---

## 12. 记忆锚点

- **asyncio ≈ goroutine**：协程 + Queue + gather；threading 是次选（心智错位）；CPU 密集用 multiprocessing
- **无缓冲 chan** → asyncio.Queue()（Python 侧默认非阻塞的 put，`maxsize=1` 只是近似）
- **有缓冲 chan** → asyncio.Queue(maxsize=N)
- **`close(ch)` → 无对应**，用哨兵值 / Event
- **`select` → asyncio.wait(FIRST_COMPLETED)** + 手动 cancel pending task；超时用 `asyncio.timeout()`（3.11+）
- **`context.WithTimeout` → asyncio.wait_for / asyncio.timeout**
- **调度差异**：Go 抢占式（v1.14+），asyncio 协作式 —— **没 `await` 的地方独占事件循环**
- **千万别在协程里**：`time.sleep` / `requests.get` / `open().read()` 大文件 / `subprocess.run` —— 全是阻塞事件循环
- **create_task 保留强引用**，否则 GC 可能收掉未完成的 task
- **取消是异常传播**：`asyncio.CancelledError`，别被 `except Exception` 吃掉
- **Semaphore/Lock/timeout/gather** 是 asyncio 相对 Go 语法更优的几处
- **口头模板**：goroutine→协程 / chan→Queue / WaitGroup→gather / select→wait+FIRST_COMPLETED / ctx cancel→Task.cancel + CancelledError
