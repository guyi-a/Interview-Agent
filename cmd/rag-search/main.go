// rag-search: 命令行 sanity 检索工具。
//
// 用法：
//   go run ./cmd/rag-search "redis 缓存穿透"                    # 默认 vec
//   go run ./cmd/rag-search -mode=bm25 "MySQL 索引"            # 关键词
//   go run ./cmd/rag-search -mode=hybrid "缓存穿透"             # RRF 融合
//   go run ./cmd/rag-search -k=10 -full "goroutine 调度"
//
// 目的：肉眼评估召回质量、验证 Retriever 端到端可用。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/rag/embedding"
	"github.com/guyi-a/Interview-Agent/internal/rag/retriever"
	"github.com/guyi-a/Interview-Agent/internal/rag/store"
)

const previewRunes = 500

func main() {
	kFlag := flag.Int("k", 5, "topK")
	dbFlag := flag.String("db", "", "sqlite 路径（默认 RAG_DB_PATH）")
	modeFlag := flag.String("mode", "vec", "检索模式：vec（向量）/ bm25（关键词）/ hybrid（RRF 融合）")
	fullFlag := flag.Bool("full", false, "打印完整 chunk，不截断")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: rag-search [-mode vec|bm25|hybrid] [-k N] [-db path] [-full] <query...>")
		flag.PrintDefaults()
	}
	flag.Parse()

	query := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if query == "" {
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	dbPath := firstNonEmpty(*dbFlag, cfg.Rag.DBPath, "data/rag.db")

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var r retriever.Retriever
	switch *modeFlag {
	case "vec":
		if !cfg.Embedding.Enabled() {
			log.Fatal("EMBEDDING_API_KEY 未配置")
		}
		emb := embedding.New(cfg.Embedding)
		r = retriever.NewBruteForce(db, emb)
	case "bm25":
		r = retriever.NewBM25(db)
	case "hybrid":
		if !cfg.Embedding.Enabled() {
			log.Fatal("EMBEDDING_API_KEY 未配置（hybrid 需要 vector 分支）")
		}
		emb := embedding.New(cfg.Embedding)
		r = retriever.NewHybrid(
			retriever.NewBruteForce(db, emb),
			retriever.NewBM25(db),
		)
	default:
		log.Fatalf("未知 mode=%q，支持：vec / bm25 / hybrid", *modeFlag)
	}

	start := time.Now()
	hits, err := r.Search(context.Background(), query, *kFlag)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	elapsed := time.Since(start).Truncate(time.Millisecond)

	fmt.Printf("query: %s\n", query)
	fmt.Printf("mode: %s  db: %s  k=%d\n", *modeFlag, dbPath, *kFlag)
	if *modeFlag == "vec" {
		fmt.Printf("embedding model: %s\n", cfg.Embedding.Model)
	}
	fmt.Printf("hits: %d  elapsed: %v\n\n", len(hits), elapsed)

	if len(hits) == 0 {
		fmt.Println("(无命中)")
		return
	}

	for i, h := range hits {
		fmt.Printf("─── [%d] score=%.4f  %s#%d ───\n", i+1, h.Score, shortPath(h.Path), h.Ord)
		content := h.Content
		if !*fullFlag {
			content = truncateRunes(content, previewRunes)
		}
		fmt.Println(content)
		fmt.Println()
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

func shortPath(p string) string {
	parts := strings.Split(p, string(os.PathSeparator))
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], string(os.PathSeparator))
}

func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "\n…（已截断，加 -full 查看完整）"
}

