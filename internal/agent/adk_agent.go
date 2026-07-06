package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/checkpoint"
	"github.com/guyi-a/Interview-Agent/internal/agent/prompts"
	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// 这两个 agent name 是稳定标识：
//   - SupervisorAgentName 用作 SSE 翻译的根 agent 白名单（adk_handler 只渲染根 agent 的事件）
//   - DeepResearchAgentName 同时是 sub-agent 的标识符，也是模型看到的工具名
const (
	SupervisorAgentName   = "supervisor"
	DeepResearchAgentName = "deep_research"
	JobSearchAgentName    = "job_search"
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
	skillLoader *skills.Loader,
	checkpointRepo *repository.CheckpointRepo,
	approvalModes *approval.ModeStore,
	classifier *approval.Classifier,
) (*ADKBundle, error) {
	if cm == nil {
		return nil, fmt.Errorf("ToolCallingChatModel is nil")
	}
	supervisorInstruction := prompts.WithSkillsIndex(prompts.Supervisor, skillLoader)
	deepResearchInstruction := prompts.WithSkillsIndex(prompts.DeepResearch, skillLoader)

	// 1) 后台研究员
	//    - 不带 Backend：继续用我们自己的 workspace/fs 工具（baseTools），不引入 ADK 原生 filesystem
	//    - WithoutWriteTodos: 默认 todos 中间件会强行注入一堆 tool/prompt，先关掉
	//    - WithoutGeneralSubAgent: 不让 deep agent 再 spawn 子 agent
	deepAgent, err := deep.New(ctx, &deep.Config{
		Name:                   DeepResearchAgentName,
		Description:            "后台研究员：处理需要多步分析、规划、生成结构化报告的复杂任务（项目分析、生成题库、写学习计划等）。不要用于普通一问一答。",
		ChatModel:              cm,
		Instruction:            deepResearchInstruction,
		MaxIteration:           50,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: baseTools,
				// approval MUST come before toolerr — toolerr also passes
				// interrupt errors through, but the correct order avoids
				// relying on that safety net.
				ToolCallMiddlewares: []compose.ToolMiddleware{
					approval.Middleware(approvalModes, classifier),
					toolerr.Middleware(),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("deep.New: %w", err)
	}

	// 2) 把 deep agent 包成 supervisor 的一个工具
	deepTool := adk.NewAgentTool(ctx, deepAgent)

	// 3) 招聘搜索员：跟 deep_research 平级的另一个 sub-agent。
	//    工具集共用 baseTools（主要用 browser_bridge + load_skill）。
	jobAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        JobSearchAgentName,
		Description: "招聘信息搜索员：所有招聘/岗位/求职/找工作/Boss直聘类任务的**唯一入口**，supervisor 遇到这类请求必须走这里，不要自己调 browser_bridge 或 browser_use 硬走。给 request 传用户意图（岗位方向、城市、级别、想要几个），我会加载 bosszp skill、检查登录、抓取、返回结构化职位列表。",
		Instruction: prompts.WithSkillsIndex(prompts.JobSearch, skillLoader),
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: baseTools,
				ToolCallMiddlewares: []compose.ToolMiddleware{
					approval.Middleware(approvalModes, classifier),
					toolerr.Middleware(),
				},
			},
		},
		MaxIterations: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent(job_search): %w", err)
	}
	jobTool := adk.NewAgentTool(ctx, jobAgent)

	// 4) Supervisor 工具列表 = baseTools + deep + job_search
	supervisorTools := make([]tool.BaseTool, 0, len(baseTools)+2)
	supervisorTools = append(supervisorTools, baseTools...)
	supervisorTools = append(supervisorTools, deepTool, jobTool)

	supervisor, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        SupervisorAgentName,
		Description: "通用生产力助手，必要时委派复杂分析任务给 deep_research。",
		Instruction: supervisorInstruction,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: supervisorTools,
				ToolCallMiddlewares: []compose.ToolMiddleware{
					approval.Middleware(approvalModes, classifier),
					toolerr.Middleware(),
				},
			},
			// Bubble up sub-agent (deep_research) internal events to the
			// Runner's iter so the UI can show real-time progress.
			EmitInternalEvents: true,
		},
		MaxIterations: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           supervisor,
		EnableStreaming: true,
		CheckPointStore: checkpoint.NewDBStore(checkpointRepo),
	})

	return &ADKBundle{
		Runner:   runner,
		RootName: SupervisorAgentName,
	}, nil
}
