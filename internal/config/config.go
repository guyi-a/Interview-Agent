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
	// Multimodal reports whether the main model accepts Anthropic-shape
	// image content blocks. Off by default because most non-Claude models
	// available on our gateway either reject or silently ignore them.
	// When off, upstream code (multimodal.BuildUserMessage) rewrites
	// [image:] attachment markers into [file:] so they flow through the
	// text-based OCR reader path instead.
	Multimodal bool
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

// EmbeddingConfig targets an OpenAI-compatible /embeddings endpoint used by
// the RAG layer to encode chunks and queries. Default deployment is Aliyun
// DashScope in "compatible-mode" (same wire shape as OpenAI's endpoint),
// but any OpenAI-compatible embedding service works.
//
// BatchSize caps how many inputs go in one request — DashScope's compatible
// mode currently limits text-embedding-v3 to 10 per call, so the client
// auto-chunks larger inputs. Dimensions is sent when the model supports
// truncated output (v3/v4); providers that ignore it just return native dim.
type EmbeddingConfig struct {
	APIKey         string
	BaseURL        string
	Model          string
	Dimensions     int
	BatchSize      int
	TimeoutSeconds int
}

func (c EmbeddingConfig) Enabled() bool {
	return c.APIKey != "" && c.BaseURL != "" && c.Model != ""
}

// RagConfig 只管 RAG 层路径/切分参数。embedding 相关继续走 EmbeddingConfig。
type RagConfig struct {
	DocsDir      string // markdown 源目录
	DBPath       string // rag.db 文件路径
	ChunkSize    int
	ChunkOverlap int
}

// SearchConfig 装联网搜索的 provider API keys。
// Tavily / Bocha 任何一个配了就能用；两个都没配就不注册 web_search 工具（agent 感知不到）。
// 环境变量名对齐 pentaloom：TAVILY_API_KEY / BOCHA_API_KEY，用户可以直接复用之前的配置。
type SearchConfig struct {
	TavilyAPIKey string
	BochaAPIKey  string
}

func (c SearchConfig) Enabled() bool {
	return c.TavilyAPIKey != "" || c.BochaAPIKey != ""
}

type Config struct {
	LLM          LLMConfig
	ApprovalFast ApprovalFastConfig
	Embedding    EmbeddingConfig
	Rag          RagConfig
	Search       SearchConfig
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
			Multimodal:     getEnvBool("LLM_MULTIMODAL", false),
		},
		ApprovalFast: ApprovalFastConfig{
			APIKey:         os.Getenv("DEEPSEEK_API_KEY"),
			BaseURL:        getEnv("APPROVAL_FAST_BASE_URL", "https://api.deepseek.com"),
			Model:          getEnv("APPROVAL_FAST_MODEL", "deepseek-chat"),
			MaxTokens:      getEnvInt("APPROVAL_FAST_MAX_TOKENS", 512),
			TimeoutSeconds: getEnvInt("APPROVAL_FAST_TIMEOUT", 15),
		},
		Embedding: EmbeddingConfig{
			APIKey:         os.Getenv("EMBEDDING_API_KEY"),
			BaseURL:        getEnv("EMBEDDING_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			Model:          getEnv("EMBEDDING_MODEL", "text-embedding-v3"),
			Dimensions:     getEnvInt("EMBEDDING_DIMENSIONS", 1024),
			BatchSize:      getEnvInt("EMBEDDING_BATCH_SIZE", 10),
			TimeoutSeconds: getEnvInt("EMBEDDING_TIMEOUT", 30),
		},
		Rag: RagConfig{
			DocsDir:      getEnv("RAG_DOCS_DIR", "docs/rag_docs"),
			DBPath:       getEnv("RAG_DB_PATH", "data/rag.db"),
			ChunkSize:    getEnvInt("RAG_CHUNK_SIZE", 500),
			ChunkOverlap: getEnvInt("RAG_CHUNK_OVERLAP", 80),
		},
		Search: SearchConfig{
			TavilyAPIKey: os.Getenv("TAVILY_API_KEY"),
			BochaAPIKey:  os.Getenv("BOCHA_API_KEY"),
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
	// Walk up from cwd until we find a .env or hit filesystem root.
	// Tests can live 3+ dirs deep (internal/rag/embedding/...) so a
	// fixed 2-level lookup wasn't enough. Cap the walk to avoid climbing
	// past the repo when run from an unexpected cwd.
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for range 8 {
		candidate := filepath.Join(dir, ".env")
		if _, err := os.Stat(candidate); err == nil {
			_ = godotenv.Overload(candidate)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
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
