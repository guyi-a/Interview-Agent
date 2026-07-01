package tools

import (
	"context"
	"fmt"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// resolveConversationWorkspace returns the absolute workspace path bound to
// the current conversation, or an error whose message tells the agent to
// call create_workspace first. Shared by fs and browser_use tools so both
// exhibit the same "no workspace → self-heal via create_workspace" flow.
func resolveConversationWorkspace(
	ctx context.Context,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
) (string, error) {
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return "", fmt.Errorf("internal error: no conversation in context")
	}
	conv, err := convRepo.Get(ctx, convID)
	if err != nil {
		return "", fmt.Errorf("load conversation: %w", err)
	}
	if conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "", fmt.Errorf("no workspace mounted for this conversation. Call create_workspace first")
	}
	project, err := projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load project: %w", err)
	}
	if project == nil {
		return "", fmt.Errorf("project %q referenced by conversation no longer exists", *conv.ProjectID)
	}
	return project.Workspace, nil
}
