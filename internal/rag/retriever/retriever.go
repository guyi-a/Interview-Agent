// Package retriever 从 rag.db 检索与 query 语义最相近的 chunk。
//
// 当前实现：BruteForceRetriever（全表扫向量、Go 里算 cosine、min-heap 取 topK、
// 回表拿 content/path）和 BM25（内存倒排 + BM25 打分）。上层通过 Retriever
// 接口调用，将来加 hybrid/rerank 都作为新实现，agent 层不动。
package retriever

import "context"

type Hit struct {
	ChunkID int64
	DocID   int64
	Path    string
	Ord     int // chunk 在原文档内的顺序，同文档多 chunk 命中时按此排回原顺序
	Content string
	Score   float64 // cosine [-1,1]，越大越相关
}

type Retriever interface {
	Search(ctx context.Context, query string, topK int) ([]Hit, error)
}
