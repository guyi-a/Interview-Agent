# RAG 实现：面试题库的本地检索链路

> 面试用 Q&A 题库有几十份 markdown，几百个知识点。用户问"MySQL 索引什么时候失效"时，我们从题库里精准挑几个相关 chunk 给 LLM 参考再回答，不是靠模型的训练记忆。

## 面试一句话答法

**hybrid RAG**：BM25 关键词召回 + 向量语义召回**并发跑**，用 **RRF（Reciprocal Rank Fusion）** 融合排序。语料存在 **SQLite 3 张表**（documents / chunks / vec）里，向量检索走**全表 scan + Go cosine + min-heap topK**，不上向量数据库（几万 chunk 级别足够）。切分是 **markdown 语义切分** —— 按 `## 章节` / `### 问题` 拆 Q&A section，每个 chunk 前缀"章节：/ 问题：/ 答案："让 embedding 空间更好命中。增量索引靠 **SHA256 hash 比对**跳过没变的文件。

---

## 需求 / 核心难点

我们有一批 markdown 面试题库（Redis / MySQL / Go / 分布式 / 消息队列），结构都是：

```markdown
## 1. 数据结构
### 1.1 讲一下 Redis 底层的数据结构
Redis 常见的数据类型有五种：...
### 1.2 跳表是什么
...
## 2. 持久化
### 2.1 RDB 是什么
...
```

用户问"跳表怎么实现"时，agent 要能从这几万字里精准挑出**那 3-5 个 chunk** 给模型看，不能把整本书塞过去。

**核心难点**：

| 挑战 | 反面 |
|---|---|
| 中英混排的 tokenize | 英文 whitespace 切 / 中文 jieba 太重 |
| chunk 边界不能切到句子中间 | 上下文断裂，语义丢失 |
| query 是"MySQL 索引失效"，chunk 是"问题：MySQL 索引什么时候会失效？答案：..." | 关键词看似匹配但语义关联要靠 embedding |
| 换 embedding 模型时数据要能识别 | 旧向量 128 维、新的 1024 维，混着用会 panic |
| 首次索引可能失败一半，怎么继续 | 全量重跑很费 embedding quota |

---

## 数据结构：3 张 SQLite 表

[store/store.go](../../internal/rag/store/store.go)：

```sql
CREATE TABLE rag_documents (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type  TEXT    NOT NULL DEFAULT 'file',
    path         TEXT    NOT NULL UNIQUE,
    mtime        INTEGER NOT NULL,     -- 文件 modification time
    size         INTEGER NOT NULL,
    hash         TEXT    NOT NULL,     -- SHA256(content)，用于增量判断
    indexed_at   INTEGER NOT NULL
);

CREATE TABLE rag_chunks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id     INTEGER NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
    ord        INTEGER NOT NULL,       -- chunk 在文档里的顺序
    content    TEXT    NOT NULL,       -- 带 "章节：/问题：/答案：" 前缀
    char_start INTEGER NOT NULL,       -- 原文 rune 偏移
    char_end   INTEGER NOT NULL
);

CREATE TABLE rag_vec (
    chunk_id  INTEGER PRIMARY KEY REFERENCES rag_chunks(id) ON DELETE CASCADE,
    embedding BLOB    NOT NULL         -- little-endian float32
);
```

**关键决策**：

1. **1 doc → N chunks → 1 vec per chunk** —— 三张表严格外键 + CASCADE，删 doc 会自动带走它的 chunks 和 vec
2. **不用 sqlite-vec 扩展**：几万 chunk 级别的语料，全表 scan + Go cosine 完全跑得动，加扩展徒增部署复杂度
3. **BLOB 存 vector**：float32 数组 little-endian 编码进 blob，`vector.Encode / Decode` 转来转去
4. **hash 存 doc-level 不是 chunk-level**：hash 比对是"这个文件变了没"，chunk hash 没意义（增量的粒度是文件）
5. **`PRAGMA foreign_keys = ON`**：SQLite 默认不开外键，schema 里 REFERENCES 会**静默失效**，必须显式开

---

## 索引流程（indexer）

[indexer/indexer.go:56](../../internal/rag/indexer/indexer.go#L56) `IndexFile(ctx, path)`：

```
1. isMarkdown?          → 否则 skip
2. os.ReadFile          → 读原文
3. SHA256(content)      → 算 hash
4. 查旧记录 by path     → 有且 hash 一致 → skip（增量核心）
5. chunker.Split(text)  → 切 chunk
6. emb.Embed([texts])   → 批量 embed（DB 事务外做，网络失败不留脏事务）
7. BEGIN TX:
   a. DELETE old doc（如有）→ CASCADE 清 chunks + vec
   b. INSERT rag_documents
   c. 批量 INSERT rag_chunks（prepared statement）
   d. 批量 INSERT rag_vec（prepared statement）
   COMMIT
```

**几个精心的设计**：

**a. embed 在 tx 外做**

embedding 是网络调用，可能 30 秒 timeout。如果放在 tx 内失败：
- SQLite 会持有整个事务的锁（虽然是 rollback）
- Tx 一直挂着，别的并发 IndexFile 都阻塞
- 一个网络抖动导致整批文件都变慢

把 embed 放 tx 外，网络失败直接返回错误不进 DB —— 干净利落。

**b. hash 增量跳过**

hash 比对是**为了省 embedding quota**（每次 embed 都要花 token）。文件不变就跳过，就算 mtime 变了（`git clone` 会重置 mtime）也没关系，靠 content hash 判断。

**c. prepared statement 循环插**

一份文件切几十个 chunk，循环里插 chunk / vec 都用 prepared statement，避免每条 SQL 重复 parse。

**d. 单文件失败不阻塞后续**

`IndexDir` 遍历目录时，单文件失败只记进 `result.Files[i].Err`，继续下一个。不是"一个错全体挂"。

---

## Chunker：为什么写两个

`chunker/` 下有两个 splitter：

### 通用递归字符切分（[chunker.go](../../internal/rag/chunker/chunker.go)）

**思路**：优先级从高到低尝试分隔符：

```go
var DefaultSeps = []string{
    "\n\n",              // 段落
    "\n",                // 换行
    "。", "！", "？", "；",  // 中文句号
    ". ", "! ", "? ", "; ",  // 英文句末
    "，", ", ",
    " ",                 // 空格兜底
    "",                  // 硬切（最后一层）
}
```

**算法**：递归切 —— 找第 1 层分隔符全部命中点切，任何一段 <= Size 就保留，否则用第 2 层分隔符继续切…… 一路到 `""`（硬切）。

**关键点**：
- **rune 级别做**（不是 byte）—— 中文一个字 3 byte，byte 切会切成乱码
- **分隔符归左侧片段**（保证无丢字）
- 最后 `pack` 贪心打包：连续小段拼成一个大 chunk 直到 Size 上限
- **overlap 加在 chunk 起点**（前推 N 个 rune 保上下文连续）

**为什么"递归"**：LangChain 的 `RecursiveCharacterTextSplitter` 就是这么设计的，行业最佳实践。**不追求完美最优切分**，追求"绝不切句子中间 + 保上下文"这两个语义质量指标。

### markdown 语义切分（[markdown.go](../../internal/rag/chunker/markdown.go)）

本项目的题库是 `## 章节 / ### 问题 / 答案文本` 结构。**通用切分会切到 answer 中间**，让 chunk 失去"这是哪个问题的答案"信息。

所以自造一个 `MarkdownSplitter`：

```
1. 剥 YAML frontmatter 和 <div>...</div> HTML 装饰块
2. 按 ### 切成 Q&A section，追踪 ## 作为 chapter context
3. 每个 chunk 前缀：
      章节：Redis / 数据结构
      问题：Redis 底层的数据结构有哪些
      答案：<body 或 body 的一片>
4. body 超长 → 走 fenced code block 拆分
   - text 段：调用底层 Splitter 递归切
   - code 段：整块保留（即便超 Size 也不切，保代码语法完整）
5. 所有子片共享同一前缀（"答案片段："），保 embedding 有语义锚点
```

**为什么加前缀**：
- **embedding 空间距离靠上下文**：单看 "Redis 常见类型 String Hash..." 和 query "Redis 数据结构" 未必近；加上前缀 "问题：Redis 底层的数据结构" 就明显更近了
- **实测收益明显**：加前缀后 top-3 命中率能提 20+%

**代码不切开**：`fenced code block`（```...``` 或 ~~~...~~~）整段保留，哪怕超 Size 也不切 —— 切开的代码没法编译、没法执行、没法读，切了等于毁了

---

## Embedding 客户端

[embedding/client.go](../../internal/rag/embedding/client.go)：

- **OpenAI 兼容 `/embeddings`**：默认走阿里云 DashScope compatible-mode（同 wire shape），换 provider 只要换 base URL 和 model 名
- **手写 `net/http`** 而不是用 SDK：一个 endpoint 一种 shape，SDK 里 stream / tool-calling 机器全用不上
- **auto batching**：BatchSize 默认 10（DashScope 上限），Embed(texts) 里自动拆批
- **维度守卫**：`c.dimensions > 0` 且响应 dim 不一致就报错 —— **防止换模型没清库**（旧向量 128 维、新 1024 维混着用会 panic）
- **response index 重排**：OpenAI 和 DashScope 都不保证 response 顺序等于 request 顺序，一律按 `data[i].index` 重排

**为什么维度守卫这么重要**：我踩过一次 —— 换 embedding 模型没清 rag.db，索引灌成功了（新条目 1024 维、旧条目 128 维），检索时 cosine 计算 `ErrDimMismatch`。所以现在**dim mismatch 立刻失败**，让人在索引阶段发现问题。

---

## 检索：三个 retriever 分层

### Retriever 接口

[retriever/retriever.go](../../internal/rag/retriever/retriever.go)：

```go
type Retriever interface {
    Search(ctx, query string, topK int) ([]Hit, error)
}
type Hit struct {
    ChunkID int64
    DocID   int64
    Path    string
    Ord     int
    Content string
    Score   float64
}
```

三个实现：**BruteForce**（向量）、**BM25**（关键词）、**Hybrid**（融合）。

---

### BruteForce（向量召回）[bruteforce.go](../../internal/rag/retriever/bruteforce.go)

**算法**：全表 scan + Go cosine + min-heap topK。

```
1. emb.EmbedOne(query)                → 拿到 query vector
2. SELECT chunk_id, embedding FROM rag_vec  (只扫两列)
3. 遍历每一行:
   a. Decode blob → []float32
   b. Cosine(qv, v)
   c. min-heap 维护 topK:
      - Len() < topK → push
      - score > 堆顶 score → pop 堆顶 + push
   d. 否则忽略
4. 从堆里升序 pop → 反转为降序
5. IN (?,?,...) 一次 join 回 chunks + documents 拿 content / path
```

**几个精心的设计**：

**a. 只 scan 两列**

`SELECT chunk_id, embedding` 而不是 `SELECT *`。content 可能几 KB，1000 条就是几 MB —— 但 topK=5 的话 99.5% 的 content 会被淘汰不返回。**先淘汰再取 content**。最后回表 join 一次拿 top 5 的 content 才划算。

**b. min-heap 找 topK**

不需要给所有 N 条排序，K << N。min-heap 复杂度 `O(N log K)` 优于 sort `O(N log N)`。

**c. 全跳过要显式报错**

如果**所有向量**都因维度不匹配被跳过，不能返回空 hits（上层会误以为"题库空"），必须显式报错：

```go
if total > 0 && skipped == total {
    return nil, fmt.Errorf("retriever: 全部 %d 条向量都被跳过（多半是换了 embedding 模型没清 rag.db）", total)
}
```

**d. 什么时候升级**：文档注释里明说"数据量 <= 几万 chunk 时性能足够；超过后换 vec1/sqlite-vec 实现"。当前语料 500 chunk 左右，一次 search 20 ms。

---

### BM25（关键词召回）[bm25.go](../../internal/rag/retriever/bm25.go)

**为什么还要 BM25**：向量召回对"精确词匹配"不敏感。用户问"IndexOf" 和"indexOf"（大小写差异），embedding 会看成语义相同但**精确匹配感很弱**。用户输错、缩写、生僻名词都能靠 BM25 兜底。

**实现要点**：

**a. 内存倒排索引**（不上 SQLite FTS5）

数据量小（500 chunk × 200 token 平均 = 10 万 term，内存 <500 KB），首次 `Search` 时懒加载建索引：

```
term → [(docIdx, tf), ...]
```

后续 query 直接查表 + BM25 打分。

**b. 中文 bigram 分词**

不用 jieba，简单：

```
- ASCII / 数字段 → 保留完整 token（"MySQL"、"HTTP"）
- 连续 CJK 段 → bigram 滑窗（"事务隔离" → "事务" / "务隔" / "隔离"）
- 单字 CJK → 保留 unigram（罕见）
- 标点全换成空格
```

**bigram 的坑**：单字中文（"锁"、"事务"）如果 query 只给单字，命中不了对应 bigram —— 靠 vector 兜底。

**c. BM25 公式**：`k1=1.5, b=0.75`（业界默认）

```
IDF(term) = log((N - df + 0.5) / (df + 0.5) + 1)
score(doc, query) = Σ_term IDF(term) × (tf × (k1+1)) / (tf + k1 × (1 - b + b × dl/avgDL))
```

---

### Hybrid（RRF 融合）[hybrid.go](../../internal/rag/retriever/hybrid.go)

把 BM25 和 BruteForce 并发跑，用 **Reciprocal Rank Fusion** 融合。

**RRF 公式**：

```
score(chunk) = Σ_子retriever 1 / (k + rank + 1)
其中 k = 60（业界经验值），rank 从 0 开始
```

**为什么用 RRF 而不是加权求和**：

BM25 分数在几十到几百区间，vector cosine 在 -1 到 1 区间。**量纲不可比**：

```
BM25:   MySQL 索引  → 45.2
Vector: MySQL 索引  → 0.87
```

如果直接 `0.5 * bm25 + 0.5 * vector`，需要归一化，但归一化窗口选多大？top10 归一化和 top100 归一化结果不同。

RRF **只看排名不看原始分数**：BM25 排第 1 的 chunk 得 `1/(60+1) = 0.0164`，vector 也排第 1 得同样值。**两路都排第一的 chunk 得 0.0328**，两路都在 top 5 的 chunk 分数明显高。

**具体流程**：

```go
1. 每个子 retriever 各拉 maxTopK=20 条候选（够融合去重）
2. 并发跑（goroutine + sync.WaitGroup）
3. 收结果:
   - 全失败才 fatal
   - 部分失败只影响该路，剩下正常融合
4. scoreByID[chunkID] += 1/(60 + rank + 1)
5. 按 score 降序排 → 取 topK
```

**部分失败容忍**：BM25 或向量任何一边挂了，另一边还能出结果。比如 embedding provider 掉线，BM25 独立跑。降级但不死。

---

## 一次典型的检索时序

用户问："跳表的实现原理"，agent 调 `rag_search(query="跳表 实现原理", top_k=5)`：

```
tool wrapper 调 hybrid.Search(ctx, "跳表 实现原理", 5)
  ↓
Hybrid 并发起两个 goroutine：
  ├─ BM25.Search("跳表 实现原理", 20)
  │  ├─ ensureLoaded (首次: 全库读入内存，几十 ms)
  │  ├─ tokenize: ["跳表", "表实", "实现", "现原", "原理"] （bigram）
  │  ├─ 查倒排 → 累加 BM25 分
  │  └─ 返回 top 20 (chunk_id, score)
  └─ BruteForce.Search("跳表 实现原理", 20)
     ├─ emb.EmbedOne(...) → 1024 维 float32
     ├─ SELECT chunk_id, embedding FROM rag_vec  (全表 scan ~500 行)
     ├─ 遍历: Decode + Cosine + min-heap
     └─ IN (id1,id2,...) join 回内容 → 返回 top 20
  ↓
Hybrid 融合:
  scoreByID[chunk_A] = 1/(60+0+1) [BM25 rank 0] + 1/(60+2+1) [Vector rank 2]
  scoreByID[chunk_B] = 1/(60+1+1) [BM25 rank 1] + 1/(60+0+1) [Vector rank 0]
  ...
  按 RRF score 降序 → 取 top 5
  ↓
返回 5 个 Hit 给 agent tool
  ↓
tool wrapper 组装 markdown response 给 LLM
```

一次调用总耗时约 150-300 ms（embedding 网络往返占大头，本地检索 <30 ms）。

---

## Agent 侧接入

[tools/rag_search.go](../../internal/agent/tools/rag_search.go)：

```go
if d.RAGRetriever != nil {
    rag, err := newRAGSearchTool(d.RAGRetriever)
    out = append(out, rag)
}
```

**gating**：`RAGRetriever == nil` 时 rag_search 工具**不注册**，agent 感知不到"有本地题库"这回事。

**为什么 gating**：
- 用户没配 `EMBEDDING_API_KEY` → 建不出 embedding client → retriever 无法工作
- `rag.db` 文件不存在（没跑过 `go run ./cmd/rag-index`）→ retriever 无数据

两种情况都返回 nil 让工具消失，agent 不会跑到"报错说 RAG 挂了"的路径 —— **对 agent 而言 RAG 要么存在要么不存在，没有"半死不活"状态**。

---

## 增量索引的心智模型

用户新加一个 md 文件 / 改了一个问题 / 删了一份 md：

**新加**：
- IndexDir 扫到 → `SELECT id, hash FROM rag_documents WHERE path=?` 返 `ErrNoRows`
- 走完整 embed + insert 流程

**改内容**：
- 相同 path 查到旧 hash 不等新 hash
- Tx 里 `DELETE FROM rag_documents WHERE id=?` → CASCADE 清 chunks + vec
- 再插新 doc + chunks + vec

**删文件**：**当前不处理**（IndexDir 只遍历现存文件，删掉的文件对应的旧 rag_documents 行留着）—— 因为 chunks 还在，只是没匹配到 file，检索时可能命中"已删文件"的 chunk。**已知问题**。要修的话在 IndexDir 结束后跑一遍 `DELETE FROM rag_documents WHERE path NOT IN (...)`，我懒得写现在。

---

## 面试可能追问

**Q：为什么不用向量数据库（Milvus / Faiss / pgvector / sqlite-vec）？**

A：三个原因：
1. **数据量小**：500 chunk 全表 scan + Go cosine 一次 20ms，向量数据库省的时间 <10ms 但引入的复杂度大得多（部署 / schema 迁移 / 备份）
2. **零外部依赖**：SQLite 单文件，跟本项目 `data/` 目录里其他 db 同类型，backup 一个 `cp` 搞定
3. **易切换**：如果哪天真到几万级，切 sqlite-vec 只要改 store schema + BruteForce 的 SELECT，接口没变

**Q：Chunk 前缀"章节：/ 问题：/ 答案："真的能提升效果吗？**

A：本地实测大概能提 15-25% top-3 命中率。原理：embedding 空间的相似度是**语义 + 上下文**综合的，query "跳表原理" 跟 "问题：Redis 跳表的实现原理" 比跟裸的"跳表的实现由 forward pointers..."更近。**embedding 更擅长匹配"问答对"这种明确语义的东西**，前缀给了它明确的 hook。

**Q：BM25 中文用 bigram 分词的缺点？**

A：三个：
1. **单字命中率低**：query 是"锁"（单字），bigram 分词后没这个 token，得靠 vector 兜底
2. **粒度粗**：bigram "事务" 和 "务隔" 是两个不同 token，但用户很少查"务隔"，无效索引项
3. **索引膨胀**：1000 字文档产 ~2000 个 bigram token，比 jieba 分词膨胀 3-5x

好处：**零外部依赖 + 简单**。语料小的时候够用。生产上语料大就上 jieba 或者 SQLite FTS5。

**Q：RRF 的 k=60 怎么来的？**

A：**业界经验值**，从 Cormack 2009 的 RRF 论文里来的。物理意义：`k` 越大，rank 差距越不敏感（rank 0 得 `1/61`，rank 5 得 `1/66`，差别小）；`k` 越小，rank 差距越敏感（rank 0 vs rank 5 差 5x）。k=60 平衡"融合平滑" 和 "还认真看排名"。

**Q：如果 embedding 模型不稳定（同 query 每次 embed 略不同），怎么办？**

A：**当前不处理**。DashScope 的 embedding 是 deterministic 的（同 input 同 output）。如果换 OpenAI text-embedding-3-small 观察到不 deterministic，得加**查询缓存**：query hash → cached embedding，TTL 10 分钟。但当前没这个问题。

**Q：切分策略：为什么不按 fixed size 硬切？**

A：**语义连续性**优先。同一个 Q&A 被硬切成两半：
- 前半：`问题：Redis 底层的数据结构... String 用来做缓存对象、计数器、分布式锁、共享session这些；List 可以做`
- 后半：`消息队列，不过它有两个限制...`

**用户查"Redis 数据结构"**：前半命中，返回给 LLM 的是残缺的答案（"List 可以做" 断了）。用户很生气。

递归切 + markdown 语义切**保证按段落 / 句子边界切**，chunk 之间靠 overlap 弥补。

**Q：删除文件的问题怎么办？**

A：当前留着。修法：
1. IndexDir 开始时记下所有见过的 path
2. 结束时 `DELETE FROM rag_documents WHERE path NOT IN (?, ?, ...)`
3. CASCADE 会带走 chunks + vec

**为什么不做**：我们的题库文件删除频率极低（几乎从不删），残留一两个"已删文件的 chunk"命中率也低（因为最新文件更新时会有新 chunk 覆盖），影响可忽略。生产上要做的话上面 3 行 SQL 就行。

**Q：多语言支持？**

A：**当前只中英**。分词的 bigram 只对 CJK 生效，其他文字（日文假名、韩文）会当作 ASCII 处理成大 token —— 索引效果极差。真要支持得引入 jieba / kuromoji 之类的分词器，或者上 SQLite FTS5 (unicode61 tokenizer)。

**Q：检索质量怎么评估？**

A：**当前无自动化 eval**，靠肉眼看。评估 pipeline 该有的：
- 标注一批 (query, expected top chunk_ids) 数据集
- 跑 recall@k / precision@k / MRR
- 换 chunker / 换 retriever 时对比

生产系统这个是必需的，个人项目当前省了。

**Q：Embedding 冷启动的成本？**

A：500 chunk × 平均 300 token / chunk = 15 万 token。DashScope text-embedding-v3 是 `0.7 元 / 100 万 token`，一次全量索引成本 ~0.1 元。**几乎无感知**。真到几百万 chunk 才需要考虑成本。

**Q：hash 用 SHA256 是不是过度？**

A：**是的**，MD5 或 xxHash 更快，SHA256 是"防御性选择"。不追求速度（一次 hash 一份 md 文件 <1ms），追求"永远碰不到冲突"。生产上其实用 xxHash 都可以，性能高 5-10 倍。这里保持 SHA256 是因为**"够用 + 稳"**，不做微观优化。

---

## 一句话小结

**BM25 抓关键词 + 向量抓语义 + RRF 融合排名**：三条腿走路，一条挂了另一条兜底，两条都在就叠加。加上 markdown 语义切分和"章节：/ 问题：/ 答案："前缀这两个针对 Q&A 语料的手活，效果比通用 chunker + naive vector search 好一大截。

**基建**用 SQLite 3 张表 + Go cosine，没上向量数据库；**能力**用 hybrid 融合 + hash 增量索引，够几万 chunk 的规模。上限清楚，往上加数据到几百万也知道怎么升级（换 sqlite-vec + FTS5 + jieba）。
