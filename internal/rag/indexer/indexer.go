// Package indexer 把文档灌进 rag.db：文本 → chunk → embedding → 三张表。
//
// 只支持 .md（源数据都是 markdown 面试题库）。非 md 文件直接跳过。
// hash 未变的文件跳过重建。
package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/rag/chunker"
	"github.com/guyi-a/Interview-Agent/internal/rag/embedding"
	"github.com/guyi-a/Interview-Agent/internal/rag/vector"
)

type Indexer struct {
	db      *sql.DB
	emb     *embedding.Client
	chunker chunker.Chunker
}

func New(db *sql.DB, emb *embedding.Client, ch chunker.Chunker) *Indexer {
	return &Indexer{db: db, emb: emb, chunker: ch}
}

// FileResult 单文件索引结果。CLI 输出 + 错误定位都从这里读。
type FileResult struct {
	Path     string
	Skipped  bool   // hash 未变，跳过重建
	Reason   string // 跳过原因（非 md、hash 一致、空文件等）
	Chunks   int
	Duration time.Duration
	Err      error
}

// DirResult 目录扫描汇总。
type DirResult struct {
	Total   int
	Indexed int
	Skipped int
	Failed  int
	Files   []FileResult
}

// IndexFile 索引单个文件。非 .md 返回 Skipped=true。
// 失败通过 result.Err 返回，同时 error 也非 nil，方便调用侧任选。
func (ix *Indexer) IndexFile(ctx context.Context, path string) (FileResult, error) {
	start := time.Now()
	r := FileResult{Path: path}

	abs, err := filepath.Abs(path)
	if err != nil {
		r.Err = fmt.Errorf("abs path: %w", err)
		return r, r.Err
	}
	r.Path = abs

	if !isMarkdown(abs) {
		r.Skipped = true
		r.Reason = "非 markdown"
		return r, nil
	}

	st, err := os.Stat(abs)
	if err != nil {
		r.Err = fmt.Errorf("stat: %w", err)
		return r, r.Err
	}
	if st.IsDir() {
		r.Err = fmt.Errorf("是目录不是文件")
		return r, r.Err
	}

	content, err := os.ReadFile(abs)
	if err != nil {
		r.Err = fmt.Errorf("read: %w", err)
		return r, r.Err
	}
	if len(content) == 0 {
		r.Skipped = true
		r.Reason = "空文件"
		return r, nil
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	// 查旧记录 —— hash 一致就跳过
	var existingID int64
	var existingHash string
	row := ix.db.QueryRowContext(ctx, `SELECT id, hash FROM rag_documents WHERE path = ?`, abs)
	err = row.Scan(&existingID, &existingHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 新文件
	case err != nil:
		r.Err = fmt.Errorf("查旧记录: %w", err)
		return r, r.Err
	default:
		if existingHash == hash {
			r.Skipped = true
			r.Reason = "hash 未变"
			r.Duration = time.Since(start)
			return r, nil
		}
	}

	// 切 chunk
	chunks := ix.chunker.Split(string(content))
	if len(chunks) == 0 {
		r.Skipped = true
		r.Reason = "切分后无 chunk"
		return r, nil
	}
	r.Chunks = len(chunks)

	// 批 embed —— 全在 DB 事务之前完成，网络失败不留脏事务
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}
	vecs, err := ix.emb.Embed(ctx, texts)
	if err != nil {
		r.Err = fmt.Errorf("embed: %w", err)
		return r, r.Err
	}
	if len(vecs) != len(chunks) {
		r.Err = fmt.Errorf("embed 返回 %d 条，chunk %d 条", len(vecs), len(chunks))
		return r, r.Err
	}

	// 事务：删旧 doc（CASCADE 清 chunks+vec）+ 插新 doc + chunks + vec
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		r.Err = fmt.Errorf("begin tx: %w", err)
		return r, r.Err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	if existingID != 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rag_documents WHERE id = ?`, existingID); err != nil {
			r.Err = fmt.Errorf("删旧 doc: %w", err)
			return r, r.Err
		}
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO rag_documents(source_type, path, mtime, size, hash, indexed_at) VALUES(?,?,?,?,?,?)`,
		"file", abs, st.ModTime().Unix(), st.Size(), hash, time.Now().Unix(),
	)
	if err != nil {
		r.Err = fmt.Errorf("插 doc: %w", err)
		return r, r.Err
	}
	docID, err := res.LastInsertId()
	if err != nil {
		r.Err = fmt.Errorf("拿 doc id: %w", err)
		return r, r.Err
	}

	// 预编译 chunk / vec insert 语句，循环里少反复解析 SQL
	stChunk, err := tx.PrepareContext(ctx,
		`INSERT INTO rag_chunks(doc_id, ord, content, char_start, char_end) VALUES(?,?,?,?,?)`)
	if err != nil {
		r.Err = fmt.Errorf("prep chunk: %w", err)
		return r, r.Err
	}
	defer stChunk.Close()
	stVec, err := tx.PrepareContext(ctx,
		`INSERT INTO rag_vec(chunk_id, embedding) VALUES(?,?)`)
	if err != nil {
		r.Err = fmt.Errorf("prep vec: %w", err)
		return r, r.Err
	}
	defer stVec.Close()

	for i, c := range chunks {
		res, err := stChunk.ExecContext(ctx, docID, c.Ord, c.Content, c.CharStart, c.CharEnd)
		if err != nil {
			r.Err = fmt.Errorf("插 chunk %d: %w", i, err)
			return r, r.Err
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			r.Err = fmt.Errorf("拿 chunk %d id: %w", i, err)
			return r, r.Err
		}
		if _, err := stVec.ExecContext(ctx, chunkID, vector.Encode(vecs[i])); err != nil {
			r.Err = fmt.Errorf("插 vec %d: %w", i, err)
			return r, r.Err
		}
	}

	if err := tx.Commit(); err != nil {
		r.Err = fmt.Errorf("commit: %w", err)
		return r, r.Err
	}
	rollback = false
	r.Duration = time.Since(start)
	return r, nil
}

// IndexDir 递归扫 root，对每个 .md 调 IndexFile。
// 单文件失败不阻塞后续；错误全在 result.Files[i].Err 里。
func (ix *Indexer) IndexDir(ctx context.Context, root string) (DirResult, error) {
	var out DirResult
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 目录读不了不 fatal，记一条失败
			out.Files = append(out.Files, FileResult{Path: path, Err: err})
			out.Failed++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isMarkdown(path) {
			return nil
		}
		out.Total++
		res, _ := ix.IndexFile(ctx, path)
		out.Files = append(out.Files, res)
		switch {
		case res.Err != nil:
			out.Failed++
		case res.Skipped:
			out.Skipped++
		default:
			out.Indexed++
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("walk: %w", err)
	}
	return out, nil
}

func isMarkdown(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}
