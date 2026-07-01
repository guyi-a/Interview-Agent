package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/prompts"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
)

// 这两个 agent name 是稳定标识：
//   - SupervisorAgentName 用作 SSE 翻译的根 agent 白名单（adk_handler 只渲染根 agent 的事件）
//   - DeepResearchAgentName 同时是 sub-agent 的标识符，也是模型看到的工具名
const (
	SupervisorAgentName   = "supervisor"
	DeepResearchAgentName = "deep_research"
)

// ADKBundle 把 root agent 和 runner 一起暴露给上层。
// runner 是给 ChatService 用的入口；rootAgent 只暴露 Name() 给 SSE handler
// 用来做"只渲染根 agent 事件"的判断。
type ADKBundle struct {
	Runner   *adk.Runner
	RootName string
}

// NewInterviewADKAgent 装配 Supervisor + DeepResearch 的双 agent 拓扑：
//
//	Runner
//	└── Supervisor (ChatModelAgent, root)
//	    ├── baseTools...                         // workspace / fs / 其他业务工具
//	    └── deep_research (DeepAgent wrapped via NewAgentTool)
//
// EmitInternalEvents=true 让 sub-agent 内部事件冒泡到 Runner 的 iter，
// adk_handler 会把它们翻译成带 agent 字段的 SSE 帧，UI 展示 deep_research
// 在干嘛，持久化时塞进 message.Extra.sub_events 数组（带
// parent_tool_call_id 链接回 root 的 deep_research 工具卡片）。
func NewInterviewADKAgent(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	baseTools []tool.BaseTool,
) (*ADKBundle, error) {
	if cm == nil {
		return nil, fmt.Errorf("ToolCallingChatModel is nil")
	}

	// 1) 后台研究员
	//    - 不带 Backend：继续用我们自己的 workspace/fs 工具（baseTools），不引入 ADK 原生 filesystem
	//    - WithoutWriteTodos: 默认 todos 中间件会强行注入一堆 tool/prompt，先关掉
	//    - WithoutGeneralSubAgent: 不让 deep agent 再 spawn 子 agent
	deepAgent, err := deep.New(ctx, &deep.Config{
		Name:                   DeepResearchAgentName,
		Description:            "后台研究员：处理需要多步分析、规划、生成结构化报告的复杂任务（项目分析、生成题库、写学习计划等）。不要用于普通一问一答。",
		ChatModel:              cm,
		Instruction:            prompts.DeepResearch,
		MaxIteration:           16,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: baseTools,
				// Without this, a tool error fatals the whole AgentEvent stream
				// — model never sees the error and can't recover.
				ToolCallMiddlewares: []compose.ToolMiddleware{toolerr.Middleware()},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("deep.New: %w", err)
	}

	// 2) 把 deep agent 包成 supervisor 的一个工具
	deepTool := adk.NewAgentTool(ctx, deepAgent)

	// 3) Supervisor 工具列表 = baseTools + deepTool
	//    复制一份，避免修改调用方的 slice
	supervisorTools := make([]tool.BaseTool, 0, len(baseTools)+1)
	supervisorTools = append(supervisorTools, baseTools...)
	supervisorTools = append(supervisorTools, deepTool)

	supervisor, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        SupervisorAgentName,
		Description: "通用生产力助手，必要时委派复杂分析任务给 deep_research。",
		Instruction: prompts.Supervisor,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               supervisorTools,
				ToolCallMiddlewares: []compose.ToolMiddleware{toolerr.Middleware()},
			},
			// Bubble up sub-agent (deep_research) internal events to the
			// Runner's iter so the UI can show real-time progress.
			EmitInternalEvents: true,
		},
		MaxIterations: 12,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           supervisor,
		EnableStreaming: true,
	})

	return &ADKBundle{
		Runner:   runner,
		RootName: SupervisorAgentName,
	}, nil
}
