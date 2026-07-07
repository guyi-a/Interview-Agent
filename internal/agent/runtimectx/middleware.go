package runtimectx

import (
	"context"

	"github.com/cloudwego/eino/adk"

	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// WorkspaceMiddleware 每次 agent 运行开始时把 workspace 状态拼进 instruction。
// 用 BeforeAgent hook（而非 BeforeModelRewriteState）：一次运行 = 一个用户 turn 的
// 响应循环，运行开始时的 workspace 状态就够了；本 turn 中途 create_workspace 的
// 场景 LLM 会看到 tool 返回，不必再重复注入。
//
// 嵌入 *BaseChatModelAgentMiddleware 以复用其他 hook 的 no-op 默认实现；只重写
// BeforeAgent 一个方法。
type WorkspaceMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	convRepo    *repository.ConversationRepo
	projectRepo *repository.ProjectRepo
}

func NewWorkspaceMiddleware(convRepo *repository.ConversationRepo, projectRepo *repository.ProjectRepo) *WorkspaceMiddleware {
	return &WorkspaceMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		convRepo:                     convRepo,
		projectRepo:                  projectRepo,
	}
}

// BeforeAgent 把 workspace 状态 snippet 拼到 instruction 末尾。
func (m *WorkspaceMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	ws := LoadWorkspaceInfo(ctx, m.convRepo, m.projectRepo)
	snippet := RenderSnippet(ws)
	if runCtx.Instruction != "" {
		runCtx.Instruction = runCtx.Instruction + "\n\n" + snippet
	} else {
		runCtx.Instruction = snippet
	}
	return ctx, runCtx, nil
}
