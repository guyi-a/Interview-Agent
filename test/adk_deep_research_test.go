package test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// TestADKDeepResearchAsAgentTool 验证迁移计划的目标拓扑：
//
//	Supervisor (ChatModelAgent)
//	└── DeepResearch (DeepAgent) — 通过 NewAgentTool 包成工具
//
// 关键验证点：
//  1. deep.New 能正常构造 ResumableAgent
//  2. adk.NewAgentTool 能把 DeepAgent 包成 tool.BaseTool
//  3. 把这个 tool 挂到 supervisor 的 ToolsConfig 后，supervisor 知道有 deep_research 工具
//  4. 设置 EmitInternalEvents=true 后，DeepAgent 内部事件能冒泡到 runner 的 iter
//  5. 通过 ev.AgentName 区分父子 agent 的事件
//
// 注意：这个测试会调用真实模型若干次，比较慢，慢就慢吧。
func TestADKDeepResearchAsAgentTool(t *testing.T) {
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
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}

	// 1) DeepResearch agent —— 用 deep.New 构造
	//   - WithoutWriteTodos / WithoutGeneralSubAgent 都开，第一阶段最小化
	//   - 不带 Backend，避免 ADK 原生 filesystem 介入
	//   - 不带任何工具，让它纯靠 LLM 生成结果
	deepAgent, err := deep.New(ctx, &deep.Config{
		Name:                   "deep_research",
		Description:            "后台研究员：分析项目、生成题库、生成面试报告。不要用于普通一问一答。",
		ChatModel:              cm,
		Instruction:            "你是后台研究员。用户委托什么，你就分析什么，给出结构化结果，简短即可（这是一个测试）。",
		MaxIteration:           3,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	if err != nil {
		t.Fatalf("deep.New: %v", err)
	}
	// 验证 Name/Description 接口正确返回
	if name := deepAgent.Name(ctx); name != "deep_research" {
		t.Fatalf("deep agent name mismatch: %q", name)
	}

	// 2) 把 DeepAgent 包成 tool.BaseTool
	//   - ResumableAgent embeds Agent，可以直接传给 NewAgentTool
	deepTool := adk.NewAgentTool(ctx, deepAgent)

	// 验证 ToolInfo 用的就是 agent 的 Name/Description
	info, err := deepTool.Info(ctx)
	if err != nil {
		t.Fatalf("deepTool.Info: %v", err)
	}
	if info.Name != "deep_research" {
		t.Errorf("tool name mismatch: %q", info.Name)
	}
	t.Logf("deep_research tool info: name=%s desc=%q", info.Name, info.Desc)

	// 3) Supervisor —— 普通 ChatModelAgent，工具列表里只有 deepTool
	supervisor, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "interview_supervisor",
		Description: "总面试官。",
		Instruction: "你是总面试官。当用户要求做项目分析或生成题库等复杂任务时，必须调用 deep_research 工具委派给后台研究员，不要自己回答。简短任务自己回答。",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{deepTool},
			},
			// 关键开关：让 sub-agent 的事件冒泡到 runner.iter
			EmitInternalEvents: true,
		},
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent supervisor: %v", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           supervisor,
		EnableStreaming: true,
	})

	iter := runner.Run(ctx, []*schema.Message{
		schema.UserMessage("请委派给 deep_research，让它分析一个名为 Interview-Agent 的项目（一个 Go 后端 + React 前端的 AI 助手），输出 2 条可能的面试问题即可。"),
	})

	var (
		eventCount         int
		seenAgents         = map[string]int{}
		supervisorToolCall bool
		deepResearchSeen   bool
		finalText          strings.Builder
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

		seenAgents[ev.AgentName]++
		if ev.AgentName == "deep_research" {
			deepResearchSeen = true
		}

		if ev.Output == nil || ev.Output.MessageOutput == nil {
			t.Logf("event #%d agent=%s no_output action=%+v",
				eventCount, ev.AgentName, ev.Action)
			continue
		}
		mv := ev.Output.MessageOutput
		t.Logf("event #%d agent=%s streaming=%v role=%s tool=%s",
			eventCount, ev.AgentName, mv.IsStreaming, mv.Role, mv.ToolName)

		if !mv.IsStreaming {
			if mv.Message == nil {
				continue
			}
			t.Logf("  non-stream: role=%s content=%q tool_calls=%d tool_call_id=%s",
				mv.Message.Role, truncate(mv.Message.Content, 200),
				len(mv.Message.ToolCalls), mv.Message.ToolCallID)
			continue
		}
		stream := mv.MessageStream
		if stream == nil {
			continue
		}
		fullMsg, recvErr := schema.ConcatMessageStream(stream)
		stream.Close()
		if recvErr != nil || fullMsg == nil {
			continue
		}
		t.Logf("  stream concat: content_len=%d tool_calls=%d",
			len(fullMsg.Content), len(fullMsg.ToolCalls))
		for i, tc := range fullMsg.ToolCalls {
			t.Logf("    tool_call[%d]: id=%s name=%s args=%s",
				i, tc.ID, tc.Function.Name, truncate(tc.Function.Arguments, 200))
			if ev.AgentName == "interview_supervisor" && tc.Function.Name == "deep_research" {
				supervisorToolCall = true
			}
		}
		// 只把 supervisor 最终输出（无 tool_call 的 assistant 消息）算作 final
		if ev.AgentName == "interview_supervisor" && len(fullMsg.ToolCalls) == 0 && fullMsg.Content != "" {
			finalText.WriteString(fullMsg.Content)
		}
	}

	t.Logf("=== agents seen: %+v ===", seenAgents)
	if !supervisorToolCall {
		t.Error("supervisor 没有触发 deep_research tool_call —— prompt 可能不够强或 EmitInternalEvents 没开")
	}
	if !deepResearchSeen {
		t.Error("没收到 ev.AgentName=deep_research 的事件 —— sub-agent 事件没冒泡到 runner，需要确认 EmitInternalEvents 配置")
	}
	if finalText.Len() == 0 {
		t.Log("warning: supervisor 没给出最终文本（可能 MaxIterations 不够，但 deep_research 调用本身已经被验证）")
	}
	t.Logf("=== final supervisor text ===\n%s", finalText.String())
}
