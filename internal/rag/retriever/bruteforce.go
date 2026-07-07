package retriever

import (
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/rag/embedding"
	"github.com/guyi-a/Interview-Agent/internal/rag/vector"
)

const (
	defaultTopK = 5
	maxTopK     = 20
)

// BruteForce：全表 SELECT + Go cosine + min-heap topK。
// 数据量 <= 几万 chunk 时性能足够；超过后换 vec1/sqlite-vec 实现。
type BruteForce struct {
	db  *sql.DB
	emb *embedding.Client
}

func NewBruteForce(db *sql.DB, emb *embedding.Client) *BruteForce {
	return &BruteForce{db: db, emb: emb}
}

func (b *BruteForce) Search(ctx context.Context, query string, topK int) ([]Hit, error) {
	if b.emb == nil {
		return nil, errors.New("retriever: embedding client 未配置（RAG 未启用）")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("retriever: query 为空")
	}
	if topK <= 0 {
		topK = defaultTopK
	}
	if topK > maxTopK {
		topK = maxTopK
	}

	qv, err := b.emb.EmbedOne(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("retriever: embed query: %w", err)
	}

	// 只扫 chunk_id + embedding，不带 content —— content 可能很大，
	// N-1 条会被 topK 淘汰，全部扫出来太浪费。
	rows, err := b.db.QueryContext(ctx, `SELECT chunk_id, embedding FROM rag_vec`)
	if err != nil {
		return nil, fmt.Errorf("retriever: scan vec: %w", err)
	}
	defer rows.Close()

	// min-heap 大小 = topK。堆顶是当前 topK 里最小分数；来了新的更大的就 pop 掉堆顶。
	h := &scoreHeap{}
	heap.Init(h)
	total, skipped := 0, 0
	for rows.Next() {
		total++
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, fmt.Errorf("retriever: scan row: %w", err)
		}
		v, err := vector.Decode(blob)
		if err != nil {
			skipped++
			continue
		}
		s, err := vector.Cosine(qv, v)
		if err != nil {
			// 存储向量维度和 query 维度不一致 —— 通常是换了模型忘清库。
			skipped++
			continue
		}
		// TODO: 后面加 MinScore 配置过滤负分（cosine 负 = 语义方向相反，多半噪声）。
		// 暂不过滤：语料小的时候强过滤会让 topK 空，先接受可能的噪声。
		item := scoreItem{id: id, score: float64(s)}
		if h.Len() < topK {
			heap.Push(h, item)
		} else if item.score > (*h)[0].score {
			heap.Pop(h)
			heap.Push(h, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retriever: iterate vec: %w", err)
	}
	// 全部跳过：真实原因通常是换了 embedding 模型没清库，或全库 blob 都坏。
	// 返回空 hits 会让上层误以为"没相关内容"，必须显式报错。
	if total > 0 && skipped == total {
		return nil, fmt.Errorf("retriever: 全部 %d 条向量都被跳过（多半是换了 embedding 模型没清 rag.db）", total)
	}
	if skipped > 0 {
		log.Printf("retriever: %d/%d 条向量被跳过（blob 损坏或维度不匹配）", skipped, total)
	}
	if h.Len() == 0 {
		return nil, nil
	}

	// 堆里升序 pop 得到分数升序，反转成降序返回。
	items := make([]scoreItem, h.Len())
	for i := len(items) - 1; i >= 0; i-- {
		items[i] = heap.Pop(h).(scoreItem)
	}

	// 回表 join documents 拿 path/content/ord。
	// IN (?,?,...) 手工拼占位符：sqlite 驱动不支持数组绑定。
	ids := make([]any, len(items))
	scoreByID := make(map[int64]float64, len(items))
	rankByID := make(map[int64]int, len(items))
	placeholders := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
		scoreByID[it.id] = it.score
		rankByID[it.id] = i
		placeholders[i] = "?"
	}
	sqlStr := fmt.Sprintf(
		`SELECT c.id, c.doc_id, c.ord, c.content, d.path
		 FROM rag_chunks c JOIN rag_documents d ON d.id = c.doc_id
		 WHERE c.id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	rows2, err := b.db.QueryContext(ctx, sqlStr, ids...)
	if err != nil {
		return nil, fmt.Errorf("retriever: join chunks: %w", err)
	}
	defer rows2.Close()

	hits := make([]Hit, len(items))
	for rows2.Next() {
		var h Hit
		if err := rows2.Scan(&h.ChunkID, &h.DocID, &h.Ord, &h.Content, &h.Path); err != nil {
			return nil, fmt.Errorf("retriever: scan chunk: %w", err)
		}
		h.Score = scoreByID[h.ChunkID]
		hits[rankByID[h.ChunkID]] = h
	}
	if err := rows2.Err(); err != nil {
		return nil, fmt.Errorf("retriever: iterate chunks: %w", err)
	}

	// 有可能 rag_chunks 里某条被删了但 rag_vec 还没清（理论上外键 CASCADE 会带掉；
	// 但保守起见把空位过滤掉，避免返回 ChunkID=0 的僵尸 hit）
	out := hits[:0]
	for _, h := range hits {
		if h.ChunkID != 0 {
			out = append(out, h)
		}
	}
	return out, nil
}

// --- min-heap ------------------------------------------------------------

type scoreItem struct {
	id    int64
	score float64
}

type scoreHeap []scoreItem

func (h scoreHeap) Len() int            { return len(h) }
func (h scoreHeap) Less(i, j int) bool  { return h[i].score < h[j].score } // min-heap
func (h scoreHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *scoreHeap) Push(x any)         { *h = append(*h, x.(scoreItem)) }
func (h *scoreHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
