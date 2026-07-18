package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

type LLMConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	MaxTokens int
	// EnableThinking toggles DeepSeek thinking mode via
	// extra_body {"thinking":{"type":"enabled|disabled"}}.
	EnableThinking bool
	// ReasoningEffort is DeepSeek's reasoning_effort ("high" / "max";
	// "low"/"medium" are accepted for adapter compatibility and map to high
	// on DeepSeek's side). Empty → "high" when thinking is on.
	ReasoningEffort string
	// Multimodal reports whether the main model accepts native image
	// content blocks. Off by default — DeepSeek's OpenAI path is text-first;
	// when off, multimodal.BuildUserMessage rewrites [image:] markers into
	// [file:] so they flow through OCR instead.
	Multimodal bool
}

// ApprovalFastConfig points at an OpenAI-compatible endpoint used by the
// auto-mode approval classifier. Shares DEEPSEEK_API_KEY with the main LLM
// by default but keeps its own base URL / model so the classifier can stay
// on a cheaper non-thinking model. Missing APIKey disables the classifier
// entirely — auto mode then only has the fast-path rules to work with.
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

	deepseekKey := os.Getenv("DEEPSEEK_API_KEY")
	// Optional override so main agent and classifier can use different keys
	// later without renaming the shared default.
	llmKey := getEnv("LLM_API_KEY", deepseekKey)

	cfg := &Config{
		LLM: LLMConfig{
			APIKey:          llmKey,
			BaseURL:         getEnv("LLM_BASE_URL", "https://api.deepseek.com"),
			Model:           getEnv("LLM_MODEL", "deepseek-v4-pro"),
			MaxTokens:       getEnvInt("LLM_MAX_TOKENS", 8192),
			EnableThinking:  getEnvBool("LLM_ENABLE_THINKING", true),
			ReasoningEffort: getEnv("LLM_REASONING_EFFORT", "high"),
			Multimodal:      getEnvBool("LLM_MULTIMODAL", false),
		},
		ApprovalFast: ApprovalFastConfig{
			APIKey:         deepseekKey,
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
		return nil, fmt.Errorf("DEEPSEEK_API_KEY (or LLM_API_KEY) is required")
	}
	if cfg.LLM.BaseURL == "" {
		return nil, fmt.Errorf("LLM_BASE_URL is required")
	}
	if cfg.LLM.Model == "" {
		return nil, fmt.Errorf("LLM_MODEL is required")
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
