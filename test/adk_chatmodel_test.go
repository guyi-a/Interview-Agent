package test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TestADKChatModelAgentBasic 验证最小可跑的 ChatModelAgent + Runner：
//   - NewChatModelAgent + NewRunner 能否构造
//   - Run() 返回的 AsyncIterator 能否拉取到事件
//   - 事件中能否拿到 assistant 文本（非流式路径，EnableStreaming=false）
//
// 这是迁移的最低门槛——这条路跑通才能谈替换 react.Agent。
func TestADKChatModelAgentBasic(t *testing.T) {
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

	ag, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "test_agent",
		Description: "A simple test agent.",
		Instruction: "你是一个测试助手，简短回答。",
		Model:       cm,
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           ag,
		EnableStreaming: false,
	})

	iter := runner.Run(ctx, []*schema.Message{
		schema.UserMessage("Reply with exactly the word: pong"),
	})

	var (
		eventCount       int
		assistantContent strings.Builder
		hasErr           error
	)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		eventCount++

		t.Logf("event #%d agent=%s has_output=%v has_action=%v has_err=%v",
			eventCount, ev.AgentName,
			ev.Output != nil, ev.Action != nil, ev.Err != nil)

		if ev.Err != nil {
			hasErr = ev.Err
			continue
		}

		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput
		t.Logf("  mv.IsStreaming=%v role=%s tool=%s", mv.IsStreaming, mv.Role, mv.ToolName)
		if mv.IsStreaming {
			// 非流式路径下不应出现 stream
			t.Errorf("EnableStreaming=false 时不应出现 IsStreaming=true 的事件")
			continue
		}
		if mv.Message == nil {
			continue
		}
		t.Logf("  message: role=%s content=%q reasoning=%q tool_calls=%d",
			mv.Message.Role, mv.Message.Content, mv.Message.ReasoningContent, len(mv.Message.ToolCalls))
		if mv.Role == schema.Assistant {
			assistantContent.WriteString(mv.Message.Content)
		}
	}

	if hasErr != nil {
		t.Fatalf("agent emitted error event: %v", hasErr)
	}
	if eventCount == 0 {
		t.Fatal("no events received from runner")
	}
	if assistantContent.Len() == 0 {
		t.Fatal("no assistant content collected from events")
	}
	t.Logf("=== final assistant content (events=%d) ===\n%s", eventCount, assistantContent.String())
}

// TestADKChatModelAgentStream 验证 EnableStreaming=true 下事件流形状：
//   - 事件中 MessageOutput.IsStreaming 应为 true
//   - MessageStream 能拉到多个 chunk
//   - chunk.Content / chunk.ReasoningContent 字段可用
//
// 这是我们 SSE handler 真正的输入。
func TestADKChatModelAgentStream(t *testing.T) {
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

	ag, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "stream_agent",
		Description: "Streaming test agent.",
		Instruction: "你是一个测试助手。",
		Model:       cm,
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           ag,
		EnableStreaming: true,
	})

	iter := runner.Run(ctx, []*schema.Message{
		schema.UserMessage("从 1 数到 5，每个数字之间用空格隔开。"),
	})

	var (
		eventCount    int
		streamingSeen bool
		totalChunks   int
		fullText      strings.Builder
	)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		eventCount++

		if ev.Err != nil {
			t.Fatalf("event #%d error: %v", eventCount, ev.Err)
		}

		if ev.Output == nil || ev.Output.MessageOutput == nil {
			t.Logf("event #%d: no message output (action=%v)", eventCount, ev.Action)
			continue
		}
		mv := ev.Output.MessageOutput
		t.Logf("event #%d: streaming=%v role=%s tool=%s",
			eventCount, mv.IsStreaming, mv.Role, mv.ToolName)

		if !mv.IsStreaming {
			// 在 streaming 模式下，普通 assistant 输出也走 stream
			// 但 tool-result 可能是 non-streaming
			if mv.Message != nil {
				t.Logf("  non-stream msg: role=%s content_len=%d",
					mv.Message.Role, len(mv.Message.Content))
			}
			continue
		}
		streamingSeen = true

		// 排空这个 event 的 MessageStream
		stream := mv.MessageStream
		if stream == nil {
			t.Error("IsStreaming=true 但 MessageStream==nil")
			continue
		}
		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				// io.EOF or other
				break
			}
			totalChunks++
			if chunk == nil {
				continue
			}
			if chunk.Content != "" {
				fullText.WriteString(chunk.Content)
			}
			if chunk.ReasoningContent != "" {
				t.Logf("  reasoning chunk: %q", chunk.ReasoningContent)
			}
		}
		stream.Close()
	}

	if !streamingSeen {
		t.Error("EnableStreaming=true 下没看到 IsStreaming=true 的事件")
	}
	if totalChunks == 0 {
		t.Error("没收到任何 stream chunk")
	}
	if fullText.Len() == 0 {
		t.Error("聚合文本为空")
	}
	t.Logf("=== events=%d total_chunks=%d full_text=%q ===",
		eventCount, totalChunks, fullText.String())
}
