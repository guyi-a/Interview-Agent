package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

func NewReActAgent(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	tools []tool.BaseTool,
	systemPrompt string,
) (*react.Agent, error) {
	if cm == nil {
		return nil, fmt.Errorf("ToolCallingChatModel is nil")
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("at least one tool is required for a ReAct agent")
	}

	cfg := &react.AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
		MaxStep:          12,
	}

	if systemPrompt != "" {
		cfg.MessageModifier = func(_ context.Context, input []*schema.Message) []*schema.Message {
			return append([]*schema.Message{schema.SystemMessage(systemPrompt)}, input...)
		}
	}

	return react.NewAgent(ctx, cfg)
}
