package llm

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components/model"

	"github.com/guyi-a/Interview-Agent/internal/config"
)

func NewChatModel(ctx context.Context, cfg config.LLMConfig) (model.ToolCallingChatModel, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LLMConfig.APIKey is empty")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("LLMConfig.BaseURL is empty")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("LLMConfig.Model is empty")
	}

	baseURL := cfg.BaseURL
	cc := &claude.Config{
		APIKey:    cfg.APIKey,
		BaseURL:   &baseURL,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
	}
	if cfg.EnableThinking {
		cc.Thinking = &claude.Thinking{
			Enable:       true,
			BudgetTokens: cfg.ThinkingBudget,
		}
	}
	return claude.NewChatModel(ctx, cc)
}
