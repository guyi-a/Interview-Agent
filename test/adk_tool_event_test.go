package test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type echoIn struct {
	Text string `json:"text" jsonschema:"description=Text to echo back verbatim"`
}

type echoOut struct {
	Echoed string `json:"echoed"`
}

// TestADKChatModelAgentWithTool 验证带工具时的 AgentEvent 形状：
//   - assistant 的 tool_call 事件
//   - tool 执行结果事件（Role=Tool, ToolName 非空）
//   - 最终 assistant 文本事件
//
// 这一组事件就是我们 SSE adapter 要翻译的输入。
func TestADKChatModelAgentWithTool(t *testing.T) {
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
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}

	echoTool, err := utils.InferTool(
		"echo",
		"Echo the input text back. Use this whenever the user asks you to echo something.",
		func(ctx context.Context, in echoIn) (echoOut, error) {
			return echoOut{Echoed: in.Text}, nil
		},
	)
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}

	ag, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "echo_agent",
		Description: "agent that demonstrates the echo tool.",
		Instruction: "你是一个测试助手。用户让你 echo 时，直接调用 echo 工具。",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echoTool},
			},
		},
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           ag,
		EnableStreaming: true,
	})

	iter := runner.Run(ctx, []*schema.Message{
		schema.UserMessage("请使用 echo 工具，把 hello-world 回显给我。然后用一句话总结结果。"),
	})

	var (
		eventCount    int
		assistantTxt  strings.Builder
		toolCallSeen  bool
		toolResultMsg *schema.Message
		toolResultRaw strings.Builder
		toolName      string
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
			t.Logf("event #%d: no message output (action=%+v)", eventCount, ev.Action)
			continue
		}
		mv := ev.Output.MessageOutput
		t.Logf("event #%d: agent=%s streaming=%v role=%s tool=%s",
			eventCount, ev.AgentName, mv.IsStreaming, mv.Role, mv.ToolName)

		// Tool result events 一般是 non-streaming
		if !mv.IsStreaming {
			if mv.Message == nil {
				continue
			}
			t.Logf("  non-stream msg: role=%s content=%q tool_call_id=%s tool_calls=%d",
				mv.Message.Role, truncate(mv.Message.Content, 200),
				mv.Message.ToolCallID, len(mv.Message.ToolCalls))
			if mv.Role == schema.Tool {
				toolResultMsg = mv.Message
				toolName = mv.ToolName
			}
			continue
		}

		// streaming assistant 消息（可能含 tool_calls）
		stream := mv.MessageStream
		if stream == nil {
			t.Error("IsStreaming=true 但 MessageStream==nil")
			continue
		}
		// 累积这一段 stream，看是否包含 tool_calls
		fullMsg, recvErr := schema.ConcatMessageStream(stream)
		stream.Close()
		if recvErr != nil {
			t.Logf("  ConcatMessageStream err: %v", recvErr)
			continue
		}
		if fullMsg == nil {
			continue
		}
		t.Logf("  concat stream: role=%s content_len=%d reasoning_len=%d tool_calls=%d",
			fullMsg.Role, len(fullMsg.Content), len(fullMsg.ReasoningContent), len(fullMsg.ToolCalls))
		if len(fullMsg.ToolCalls) > 0 {
			toolCallSeen = true
			for i, tc := range fullMsg.ToolCalls {
				t.Logf("    tool_call[%d]: id=%s name=%s args=%s",
					i, tc.ID, tc.Function.Name, truncate(tc.Function.Arguments, 200))
			}
		}
		if fullMsg.Content != "" {
			assistantTxt.WriteString(fullMsg.Content)
		}
	}

	if !toolCallSeen {
		t.Error("模型没有产生 tool_call —— 可能 prompt 不够强")
	}
	if toolResultMsg == nil {
		t.Error("没收到 Role=Tool 的事件，tool 执行结果事件可能走另一条路")
	} else {
		toolResultRaw.WriteString(toolResultMsg.Content)
		t.Logf("tool_result: tool=%s content=%q", toolName, truncate(toolResultMsg.Content, 200))
	}
	if assistantTxt.Len() == 0 {
		t.Error("模型最终没有给文本回答")
	}
	t.Logf("=== events=%d final_text=%q tool_called=%v ===",
		eventCount, assistantTxt.String(), toolCallSeen)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
