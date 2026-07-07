// rag-index: 把 docs/rag_docs 下的 .md 灌进 data/rag.db。
//
// 用法：
//   go run ./cmd/rag-index                       # 用 .env 里的默认路径
//   go run ./cmd/rag-index -dir=xxx -db=yyy      # 覆盖
//
// 幂等：hash 未变的文件跳过；变了就删旧记录（CASCADE）+ 重新入库。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/rag/chunker"
	"github.com/guyi-a/Interview-Agent/internal/rag/embedding"
	"github.com/guyi-a/Interview-Agent/internal/rag/indexer"
	"github.com/guyi-a/Interview-Agent/internal/rag/store"
)

func main() {
	dirFlag := flag.String("dir", "", "markdown 源目录（默认 RAG_DOCS_DIR）")
	dbFlag := flag.String("db", "", "sqlite 文件路径（默认 RAG_DB_PATH）")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if !cfg.Embedding.Enabled() {
		log.Fatal("EMBEDDING_API_KEY 未配置，无法索引")
	}

	dir := firstNonEmpty(*dirFlag, cfg.Rag.DocsDir, "docs/rag_docs")
	dbPath := firstNonEmpty(*dbFlag, cfg.Rag.DBPath, "data/rag.db")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("mkdir db dir: %v", err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	emb := embedding.New(cfg.Embedding)
	ch := chunker.NewMarkdown(cfg.Rag.ChunkSize, cfg.Rag.ChunkOverlap)
	ix := indexer.New(db, emb, ch)

	fmt.Printf("indexing %s → %s\n", dir, dbPath)
	fmt.Printf("chunk_size=%d overlap=%d model=%s dim=%d\n\n",
		cfg.Rag.ChunkSize, cfg.Rag.ChunkOverlap, cfg.Embedding.Model, cfg.Embedding.Dimensions)

	start := time.Now()
	result, err := ix.IndexDir(context.Background(), dir)
	if err != nil {
		log.Fatalf("index dir: %v", err)
	}

	for _, f := range result.Files {
		short := shortPath(f.Path, dir)
		switch {
		case f.Err != nil:
			fmt.Printf("[FAIL] %s: %v\n", short, f.Err)
		case f.Skipped:
			fmt.Printf("[skip] %s (%s)\n", short, f.Reason)
		default:
			fmt.Printf("[ok]   %s → %d chunks (%v)\n", short, f.Chunks, f.Duration.Truncate(time.Millisecond))
		}
	}

	fmt.Printf("\ntotal=%d indexed=%d skipped=%d failed=%d  elapsed=%v\n",
		result.Total, result.Indexed, result.Skipped, result.Failed,
		time.Since(start).Truncate(time.Millisecond))
	if result.Failed > 0 {
		os.Exit(1)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// shortPath 让输出短一点：优先显示相对 root 的路径。
func shortPath(abs, root string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || rel == "" {
		return abs
	}
	return rel
}
