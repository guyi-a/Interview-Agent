// bm25.go：内存 BM25 关键词检索器。
//
//   - 从 rag_chunks/rag_documents 全量加载到内存倒排索引（首次 Search 时懒加载）
//   - 朴素分词：标点换空格 + strings.Fields，不上 jieba
//   - 中文覆盖差（"事务" "锁" 命中不了），靠 vector 语义兜底
//   - BM25 打分：k1=1.5, b=0.75（业界默认）
//
// 数据量小的时候内存占用可忽略（537 chunks ≈ 500KB）；首次加载几十 ms。
// 语料变大到几十万级别再考虑持久化倒排（SQLite FTS5 或 tantivy 之类）。
package retriever

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

type BM25 struct {
	db *sql.DB

	mu       sync.RWMutex
	loaded   bool
	docs     []bm25Doc
	index    map[string][]bm25Posting // term -> [(docIdx, tf)]
	docLen   []int
	avgDL    float64
}

type bm25Doc struct {
	ChunkID int64
	DocID   int64
	Ord     int
	Content string
	Path    string
}

type bm25Posting struct {
	docIdx int
	tf     int
}

func NewBM25(db *sql.DB) *BM25 {
	return &BM25{db: db}
}

func (r *BM25) Search(ctx context.Context, query string, topK int) ([]Hit, error) {
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

	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.docs) == 0 {
		return nil, nil
	}
	qTokens := tokenize(query)
	if len(qTokens) == 0 {
		return nil, nil
	}

	scores := make(map[int]float64, len(qTokens)*4)
	N := float64(len(r.docs))
	for _, term := range qTokens {
		postings := r.index[term]
		if len(postings) == 0 {
			continue
		}
		df := float64(len(postings))
		idf := math.Log((N-df+0.5)/(df+0.5) + 1)
		for _, p := range postings {
			tf := float64(p.tf)
			dl := float64(r.docLen[p.docIdx])
			denom := tf + bm25K1*(1-bm25B+bm25B*dl/r.avgDL)
			scores[p.docIdx] += idf * (tf * (bm25K1 + 1) / denom)
		}
	}
	if len(scores) == 0 {
		return nil, nil
	}

	type ranked struct {
		docIdx int
		score  float64
	}
	arr := make([]ranked, 0, len(scores))
	for i, s := range scores {
		arr = append(arr, ranked{docIdx: i, score: s})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].score > arr[j].score })
	if len(arr) > topK {
		arr = arr[:topK]
	}

	out := make([]Hit, len(arr))
	for i, r0 := range arr {
		d := r.docs[r0.docIdx]
		out[i] = Hit{
			ChunkID: d.ChunkID,
			DocID:   d.DocID,
			Path:    d.Path,
			Ord:     d.Ord,
			Content: d.Content,
			Score:   r0.score,
		}
	}
	return out, nil
}

// ensureLoaded 懒加载：首次 Search 时把 rag_chunks 全部读进内存建倒排。
// 简单起见不做失效：文档变了要重启进程重加载。
func (r *BM25) ensureLoaded(ctx context.Context) error {
	r.mu.RLock()
	loaded := r.loaded
	r.mu.RUnlock()
	if loaded {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return nil
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT c.id, c.doc_id, c.ord, c.content, d.path
		 FROM rag_chunks c JOIN rag_documents d ON d.id = c.doc_id`)
	if err != nil {
		return fmt.Errorf("bm25 load: %w", err)
	}
	defer rows.Close()

	var docs []bm25Doc
	var docLen []int
	index := map[string][]bm25Posting{}
	totalTokens := 0
	for rows.Next() {
		var d bm25Doc
		if err := rows.Scan(&d.ChunkID, &d.DocID, &d.Ord, &d.Content, &d.Path); err != nil {
			return fmt.Errorf("bm25 scan: %w", err)
		}
		idx := len(docs)
		tokens := tokenize(d.Content)
		docs = append(docs, d)
		docLen = append(docLen, len(tokens))
		totalTokens += len(tokens)

		// 统计词频，累加进倒排
		tf := map[string]int{}
		for _, t := range tokens {
			tf[t]++
		}
		for term, n := range tf {
			index[term] = append(index[term], bm25Posting{docIdx: idx, tf: n})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("bm25 iter: %w", err)
	}
	r.docs = docs
	r.docLen = docLen
	r.index = index
	if len(docs) > 0 {
		r.avgDL = float64(totalTokens) / float64(len(docs))
	}
	r.loaded = true
	return nil
}

// tokenize 分词：小写化 + 标点换空格，之后按 rune 走：
//   - 连续 CJK 段 → bigram 滑窗（"事务隔离" → 事务/务隔/隔离）
//   - 连续 ASCII/数字段 → 保留为一个 token（MySQL、HTTP 等）
//   - 单字 CJK 段 → 保留为 unigram（罕见，孤立单字）
//
// 中文 2 字以上短语能被 query 命中；单字中文（"锁"）除非内容里恰好独立出现，
// 否则命中不了 —— 靠 vector 兜底。
func tokenize(text string) []string {
	text = strings.ToLower(text)
	text = punctReplacer.Replace(text)

	var out []string
	runes := []rune(text)
	for i := 0; i < len(runes); {
		r := runes[i]
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if isCJK(r) {
			j := i + 1
			for j < len(runes) && isCJK(runes[j]) {
				j++
			}
			run := runes[i:j]
			if len(run) == 1 {
				out = append(out, string(run))
			} else {
				for k := 0; k+2 <= len(run); k++ {
					out = append(out, string(run[k:k+2]))
				}
			}
			i = j
			continue
		}
		// ASCII / 数字 / 其他非 CJK 非空白：收成一个 token
		j := i + 1
		for j < len(runes) && !unicode.IsSpace(runes[j]) && !isCJK(runes[j]) {
			j++
		}
		out = append(out, string(runes[i:j]))
		i = j
	}
	return out
}

// isCJK 判断是否 CJK 汉字（Han 表就够；日文/韩文可按需扩展）。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

var punctReplacer = strings.NewReplacer(
	"，", " ", "。", " ", "、", " ", "：", " ", "；", " ",
	"？", " ", "！", " ", "（", " ", "）", " ",
	"《", " ", "》", " ", "「", " ", "」", " ", "“", " ", "”", " ",
	",", " ", ".", " ", ":", " ", ";", " ",
	"?", " ", "!", " ", "(", " ", ")", " ",
	"[", " ", "]", " ", "{", " ", "}", " ",
	"<", " ", ">", " ", "|", " ", "/", " ", "\\", " ",
	"'", " ", `"`, " ", "`", " ", "*", " ", "#", " ",
	"\n", " ", "\t", " ", "\r", " ",
)
