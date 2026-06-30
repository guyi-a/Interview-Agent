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

type Config struct {
	LLM LLMConfig
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
