package retriever

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

const rrfConstant = 60

// Hybrid RRF 多路召回融合。子 retriever 并发调，失败的跳过不影响其他路。
// 融合按排名倒数累加：score = Σ 1/(k + rank)，不看子 retriever 的原始分数
// （vector cosine 和 BM25 分数量纲不可比）。
type Hybrid struct {
	retrievers []Retriever
	k          int // RRF 常数
	perFetch   int // 每个子 retriever 拉多少条候选，供融合去重
}

func NewHybrid(rs ...Retriever) *Hybrid {
	return &Hybrid{
		retrievers: rs,
		k:          rrfConstant,
		perFetch:   maxTopK,
	}
}

func (h *Hybrid) Search(ctx context.Context, query string, topK int) ([]Hit, error) {
	if len(h.retrievers) == 0 {
		return nil, errors.New("retriever: hybrid 没有子 retriever")
	}
	if topK <= 0 {
		topK = defaultTopK
	}
	if topK > maxTopK {
		topK = maxTopK
	}

	type subResult struct {
		hits []Hit
		err  error
	}
	results := make([]subResult, len(h.retrievers))
	var wg sync.WaitGroup
	for i, r := range h.retrievers {
		wg.Add(1)
		go func(i int, r Retriever) {
			defer wg.Done()
			hits, err := r.Search(ctx, query, h.perFetch)
			results[i] = subResult{hits: hits, err: err}
		}(i, r)
	}
	wg.Wait()

	// 全失败才 fatal；部分失败只影响该路召回，剩下继续融合
	allFailed := true
	var lastErr error
	for _, r := range results {
		if r.err != nil {
			lastErr = r.err
		} else {
			allFailed = false
		}
	}
	if allFailed {
		return nil, fmt.Errorf("retriever: 所有子 retriever 都失败: %w", lastErr)
	}

	// RRF 融合：按 chunk_id 去重，累加 1/(k + rank+1)
	scoreByID := map[int64]float64{}
	hitByID := map[int64]Hit{}
	for _, r := range results {
		for rank, hit := range r.hits {
			scoreByID[hit.ChunkID] += 1.0 / float64(h.k+rank+1)
			if _, ok := hitByID[hit.ChunkID]; !ok {
				hitByID[hit.ChunkID] = hit
			}
		}
	}
	if len(scoreByID) == 0 {
		return nil, nil
	}

	type ranked struct {
		id    int64
		score float64
	}
	arr := make([]ranked, 0, len(scoreByID))
	for id, s := range scoreByID {
		arr = append(arr, ranked{id: id, score: s})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].score > arr[j].score })
	if len(arr) > topK {
		arr = arr[:topK]
	}

	out := make([]Hit, len(arr))
	for i, rk := range arr {
		hit := hitByID[rk.id]
		hit.Score = rk.score // RRF score 覆盖原始分数
		out[i] = hit
	}
	return out, nil
}
