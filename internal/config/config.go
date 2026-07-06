package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

type LLMConfig struct {
	APIKey         string
	BaseURL        string
	Model          string
	MaxTokens      int
	EnableThinking bool
	ThinkingBudget int
}

// ApprovalFastConfig points at an OpenAI-compatible endpoint used by the
// auto-mode approval classifier. Kept independent of LLMConfig because the
// main model runs on the Anthropic protocol while the classifier here uses
// the OpenAI chat/completions shape (DeepSeek by default). Missing APIKey
// disables the classifier entirely — auto mode then only has the fast-path
// rules to work with, and everything else falls through to human review.
type ApprovalFastConfig struct {
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	// TimeoutSeconds bounds a single classifier call. Anything over this
	// deadline is treated as failure → deny → human review (safe default).
	TimeoutSeconds int
}

func (c ApprovalFastConfig) Enabled() bool {
	return c.APIKey != "" && c.BaseURL != "" && c.Model != ""
}

type Config struct {
	LLM          LLMConfig
	ApprovalFast ApprovalFastConfig
}

func Load() (*Config, error) {
	loadDotenv()

	cfg := &Config{
		LLM: LLMConfig{
			APIKey:         os.Getenv("ANTHROPIC_API_KEY"),
			BaseURL:        os.Getenv("ANTHROPIC_BASE_URL"),
			Model:          getEnv("ANTHROPIC_MODEL", "deepseek/deepseek-v4-pro"),
			MaxTokens:      getEnvInt("ANTHROPIC_MAX_TOKENS", 8192),
			EnableThinking: getEnvBool("ANTHROPIC_ENABLE_THINKING", true),
			ThinkingBudget: getEnvInt("ANTHROPIC_THINKING_BUDGET", 4096),
		},
		ApprovalFast: ApprovalFastConfig{
			APIKey:         os.Getenv("DEEPSEEK_API_KEY"),
			BaseURL:        getEnv("APPROVAL_FAST_BASE_URL", "https://api.deepseek.com"),
			Model:          getEnv("APPROVAL_FAST_MODEL", "deepseek-chat"),
			MaxTokens:      getEnvInt("APPROVAL_FAST_MAX_TOKENS", 512),
			TimeoutSeconds: getEnvInt("APPROVAL_FAST_TIMEOUT", 15),
		},
	}

	if cfg.LLM.APIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}
	if cfg.LLM.BaseURL == "" {
		return nil, fmt.Errorf("ANTHROPIC_BASE_URL is required")
	}
	if cfg.LLM.EnableThinking && cfg.LLM.ThinkingBudget >= cfg.LLM.MaxTokens {
		return nil, fmt.Errorf("ANTHROPIC_THINKING_BUDGET (%d) must be < ANTHROPIC_MAX_TOKENS (%d)",
			cfg.LLM.ThinkingBudget, cfg.LLM.MaxTokens)
	}
	return cfg, nil
}

func loadDotenv() {
	for _, rel := range []string{".env", "../.env", "../../.env"} {
		abs, err := filepath.Abs(rel)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			_ = godotenv.Overload(abs)
			return
		}
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
