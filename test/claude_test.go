package test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/schema"
	"github.com/joho/godotenv"
)

const testModel = "deepseek/deepseek-v4-pro"

func loadEnv(t *testing.T) {
	t.Helper()
	candidates := []string{".env", "../.env", "../../.env"}
	for _, p := range candidates {
		abs, _ := filepath.Abs(p)
		if _, err := os.Stat(abs); err == nil {
			if err := godotenv.Load(abs); err != nil {
				t.Fatalf("load %s: %v", abs, err)
			}
			t.Logf("loaded env: %s", abs)
			return
		}
	}
	t.Log("no .env found, relying on process env")
}

func TestClaudeViaNovita(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if apiKey == "" {
		t.Fatal("ANTHROPIC_API_KEY is empty")
	}
	if baseURL == "" {
		t.Fatal("ANTHROPIC_BASE_URL is empty")
	}

	ctx := context.Background()
	cm, err := claude.NewChatModel(ctx, &claude.Config{
		APIKey:    apiKey,
		BaseURL:   &baseURL,
		Model:     testModel,
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}

	resp, err := cm.Generate(ctx, []*schema.Message{
		schema.UserMessage("Reply with exactly the word: pong"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp == nil || resp.Content == "" {
		t.Fatalf("empty response: %+v", resp)
	}
	t.Logf("model=%s reply=%q", testModel, resp.Content)
}

func TestClaudeStream(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if apiKey == "" || baseURL == "" {
		t.Fatal("ANTHROPIC_API_KEY or ANTHROPIC_BASE_URL is empty")
	}

	ctx := context.Background()
	cm, err := claude.NewChatModel(ctx, &claude.Config{
		APIKey:    apiKey,
		BaseURL:   &baseURL,
		Model:     testModel,
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}

	sr, err := cm.Stream(ctx, []*schema.Message{
		schema.UserMessage("从 1 数到 5，每个数字之间用空格隔开。"),
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var (
		full   strings.Builder
		chunks int
	)
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		chunks++
		full.WriteString(chunk.Content)
		t.Logf("chunk #%d: %q", chunks, chunk.Content)
	}

	if chunks == 0 {
		t.Fatal("no chunks received")
	}
	if full.Len() == 0 {
		t.Fatal("stream returned empty content")
	}
	t.Logf("model=%s total_chunks=%d full_reply=%q", testModel, chunks, full.String())
}

func TestClaudeThinking(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if apiKey == "" || baseURL == "" {
		t.Fatal("ANTHROPIC_API_KEY or ANTHROPIC_BASE_URL is empty")
	}

	ctx := context.Background()
	cm, err := claude.NewChatModel(ctx, &claude.Config{
		APIKey:    apiKey,
		BaseURL:   &baseURL,
		Model:     testModel,
		MaxTokens: 4096,
		Thinking: &claude.Thinking{
			Enable:       true,
			BudgetTokens: 1024,
		},
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}

	question := "如果 5 个工人 5 小时建 5 堵墙，那么 100 个工人建 100 堵墙需要多少小时？请简要说明推理过程。"
	resp, err := cm.Generate(ctx, []*schema.Message{
		schema.UserMessage(question),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	thinking, ok := claude.GetThinking(resp)
	t.Logf("=== thinking (have=%v) ===\n%s", ok, thinking)
	t.Logf("=== content ===\n%s", resp.Content)

	if !ok || strings.TrimSpace(thinking) == "" {
		t.Log("WARNING: 没拿到 thinking 内容 —— 大概率 Novita 中转吞掉了 thinking 字段，或当前模型不支持扩展思考")
	}
	if resp.Content == "" {
		t.Fatal("最终回复为空")
	}
}

func TestClaudeThinkingStream(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if apiKey == "" || baseURL == "" {
		t.Fatal("ANTHROPIC_API_KEY or ANTHROPIC_BASE_URL is empty")
	}

	ctx := context.Background()
	cm, err := claude.NewChatModel(ctx, &claude.Config{
		APIKey:    apiKey,
		BaseURL:   &baseURL,
		Model:     testModel,
		MaxTokens: 4096,
		Thinking: &claude.Thinking{
			Enable:       true,
			BudgetTokens: 1024,
		},
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}

	sr, err := cm.Stream(ctx, []*schema.Message{
		schema.UserMessage("一只蜗牛白天爬上井壁 3 米，夜里滑下 2 米，井深 10 米，几天能爬出来？请说明推理。"),
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var (
		thinkBuf, replyBuf strings.Builder
		thinkChunks        int
		replyChunks        int
	)
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if t, ok := claude.GetThinking(chunk); ok && t != "" {
			thinkChunks++
			thinkBuf.WriteString(t)
		}
		if chunk.Content != "" {
			replyChunks++
			replyBuf.WriteString(chunk.Content)
		}
	}

	t.Logf("thinking_chunks=%d reply_chunks=%d", thinkChunks, replyChunks)
	t.Logf("=== thinking ===\n%s", thinkBuf.String())
	t.Logf("=== reply ===\n%s", replyBuf.String())

	if thinkChunks == 0 {
		t.Log("WARNING: 流式下没收到 thinking chunk —— Novita 中转可能未透传，或被合并到聚合包里")
	}
	if replyBuf.Len() == 0 {
		t.Fatal("流式最终回复为空")
	}
}
