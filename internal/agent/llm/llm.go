package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"github.com/guyi-a/Interview-Agent/internal/config"
)

// NewChatModel builds the main ToolCallingChatModel used by the ADK topology.
// Talks to DeepSeek (or any OpenAI-compatible host) via eino-ext's openai
// adapter. Thinking mode is wired through ExtraFields so DeepSeek's
// {"thinking":{"type":"enabled"}} lands on the wire; reasoning tokens come
// back as Message.ReasoningContent and the existing SSE "thinking" path
// renders them unchanged.
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

	maxTokens := cfg.MaxTokens
	oc := &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		// DeepSeek still honours max_tokens on chat/completions; keep the
		// legacy field so non-o1 OpenAI-compatible hosts behave the same.
		MaxTokens: &maxTokens,
	}

	extra := map[string]any{}
	if cfg.EnableThinking {
		extra["thinking"] = map[string]any{"type": "enabled"}
		effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
		if effort == "" {
			effort = "high"
		}
		switch effort {
		case "low":
			oc.ReasoningEffort = openai.ReasoningEffortLevelLow
		case "medium":
			oc.ReasoningEffort = openai.ReasoningEffortLevelMedium
		case "max":
			// DeepSeek accepts "max"; eino's typed enum only goes to high.
			extra["reasoning_effort"] = "max"
		default:
			oc.ReasoningEffort = openai.ReasoningEffortLevelHigh
		}
	} else {
		extra["thinking"] = map[string]any{"type": "disabled"}
	}
	oc.ExtraFields = extra

	return openai.NewChatModel(ctx, oc)
}
