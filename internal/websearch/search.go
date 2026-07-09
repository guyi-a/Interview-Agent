package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Region 决定走哪些 provider。
type Region string

const (
	RegionCN     Region = "cn"     // 只 Bocha
	RegionGlobal Region = "global" // 只 Tavily
	RegionBoth   Region = "both"   // 并发合并，默认
)

// Config 装两把 key，从 config.Search 传进来。
// 任何一把 key 为空则该 provider 不可用；两把都空则整个 search 服务不注册。
type Config struct {
	TavilyAPIKey string
	BochaAPIKey  string
}

// Enabled 判断服务能否启动：至少配了一把 key 才行。
func (c Config) Enabled() bool {
	return c.TavilyAPIKey != "" || c.BochaAPIKey != ""
}

// Service 是 search 的顶层入口，聚合了 provider 构造 + region 路由 + 并发合并去重。
type Service struct {
	cfg    Config
	client *http.Client
}

func NewService(cfg Config) *Service {
	return &Service{
		cfg: cfg,
		client: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
}

// Options 是 Search 的可选参数。所有字段可空 —— 空值走默认。
type Options struct {
	Region         Region
	MaxResults     int      // 单 provider 上限；both 时两边各 MaxResults，合并后可能更少。1-20，默认 10
	Timelimit      string   // d/w/m/y
	AllowedDomains []string // 跟 BlockedDomains 互斥；仅 Tavily 生效
	BlockedDomains []string
	Topic          string        // finance/news，不支持的静默忽略
	Timeout        time.Duration // 单次 HTTP 超时；默认 20s
}

// Search 是对外唯一入口。
//
// 出错时机：
//   - 参数冲突（both allowed+blocked）→ 返回错误
//   - region=global 但没配 Tavily key → 返回错误
//   - region=cn 但没配 Bocha key → 返回错误
//   - region=both 但两把 key 都没配 → 返回错误
//   - region=both 且两路都失败 → 返回错误
//   - 其他单路失败在 both 模式下容忍
func (s *Service) Search(ctx context.Context, query string, opts Options) ([]TextSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query 不能为空")
	}
	if len(opts.AllowedDomains) > 0 && len(opts.BlockedDomains) > 0 {
		return nil, errors.New("allowed_domains 和 blocked_domains 不能同时指定")
	}

	region := opts.Region
	if region == "" {
		region = RegionBoth
	}
	if region != RegionCN && region != RegionGlobal && region != RegionBoth {
		return nil, fmt.Errorf("region 必须是 cn/global/both 之一，收到 %q", region)
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 20 {
		maxResults = 20
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	tavily := s.buildProvider("tavily")
	bocha := s.buildProvider("bocha")

	switch region {
	case RegionGlobal:
		if tavily == nil {
			return nil, errors.New("未配置 TAVILY_API_KEY —— region=global 需要 Tavily。" +
				"去 https://app.tavily.com 注册免费 key，填 TAVILY_API_KEY 环境变量")
		}
		return s.runOne(ctx, tavily, query, opts, maxResults, timeout)

	case RegionCN:
		if bocha == nil {
			return nil, errors.New("未配置 BOCHA_API_KEY —— region=cn 需要 Bocha。" +
				"去 https://open.bochaai.com 注册免费试用 key，填 BOCHA_API_KEY 环境变量")
		}
		return s.runOne(ctx, bocha, query, opts, maxResults, timeout)

	case RegionBoth:
		if tavily == nil && bocha == nil {
			return nil, errors.New("region=both 但两把 key 都没配。至少配 TAVILY_API_KEY 或 BOCHA_API_KEY 之一（推荐都配）")
		}
		return s.runBoth(ctx, bocha, tavily, query, opts, maxResults, timeout)
	}
	return nil, fmt.Errorf("unreachable: region=%s", region)
}

func (s *Service) buildProvider(name string) Provider {
	switch name {
	case "tavily":
		if s.cfg.TavilyAPIKey == "" {
			return nil
		}
		return NewTavilyProvider(s.cfg.TavilyAPIKey)
	case "bocha":
		if s.cfg.BochaAPIKey == "" {
			return nil
		}
		return NewBochaProvider(s.cfg.BochaAPIKey)
	}
	return nil
}

// runOne 单 provider 执行，失败返错误（带 provider 名）。
func (s *Service) runOne(
	ctx context.Context,
	p Provider,
	query string,
	opts Options,
	maxResults int,
	timeout time.Duration,
) ([]TextSearchResult, error) {
	headers, body := p.BuildRequest(
		query, maxResults, opts.Timelimit,
		opts.AllowedDomains, opts.BlockedDomains, opts.Topic,
	)

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s marshal request: %w", p.Name(), err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.Endpoint(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s build request: %w", p.Name(), err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s 超时（%v）", p.Name(), timeout)
		}
		return nil, fmt.Errorf("%s 网络错误：%w", p.Name(), err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("%s 读响应失败：%w", p.Name(), err)
	}

	switch resp.StatusCode {
	case 401:
		return nil, fmt.Errorf("%s 鉴权失败（401）—— 检查 key 是否过期 / 拼错", p.Name())
	case 403:
		return nil, fmt.Errorf("%s 配额不足（403）—— 免费额度可能没领取或已用完", p.Name())
	case 429:
		return nil, fmt.Errorf("%s 限流（429）—— 等会儿再试或升级套餐", p.Name())
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s HTTP %d：%s", p.Name(), resp.StatusCode, truncateBytes(respBytes, 300))
	}

	var data map[string]any
	if err := json.Unmarshal(respBytes, &data); err != nil {
		return nil, fmt.Errorf("%s 返回的不是 JSON：%w", p.Name(), err)
	}

	// Bocha 在 HTTP 200 时业务 code 可能非 200（如 quota 用完返 200 + code 403 in body）。
	// 统一在这里早抛，不让 parseResponse 拿到空 pages 误判为"无结果"。
	if bizCode, ok := data["code"]; ok {
		if !isBizOK(bizCode) {
			msg := strFrom(data, "msg")
			if msg == "" {
				msg = strFrom(data, "message")
			}
			return nil, fmt.Errorf("%s 业务错误 code=%v：%s", p.Name(), bizCode, msg)
		}
	}

	return p.ParseResponse(data), nil
}

// runBoth 两路并发，部分失败容忍：一边挂另一边还能用；两边都挂才抛。
func (s *Service) runBoth(
	ctx context.Context,
	bocha, tavily Provider,
	query string,
	opts Options,
	maxResults int,
	timeout time.Duration,
) ([]TextSearchResult, error) {
	type result struct {
		batch []TextSearchResult
		err   error
		name  string
	}

	var providers []Provider
	// Bocha 优先入榜 —— 中文话题通常更切题；海外话题 Bocha 没结果会自然把位置让给 Tavily。
	if bocha != nil {
		providers = append(providers, bocha)
	}
	if tavily != nil {
		providers = append(providers, tavily)
	}

	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			batch, err := s.runOne(ctx, p, query, opts, maxResults, timeout)
			results[i] = result{batch: batch, err: err, name: p.Name()}
		}(i, p)
	}
	wg.Wait()

	var batches [][]TextSearchResult
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err.Error())
			continue
		}
		batches = append(batches, r.batch)
	}

	if len(batches) == 0 {
		return nil, fmt.Errorf("region=both 两路全失败：%s", strings.Join(errs, " | "))
	}

	return mergeDedupe(batches), nil
}

// mergeDedupe 按 href 去重合并多路结果。先到先得 —— 调用方按"优先级高的放前面"传入即可。
func mergeDedupe(batches [][]TextSearchResult) []TextSearchResult {
	seen := make(map[string]struct{})
	out := make([]TextSearchResult, 0)
	for _, batch := range batches {
		for _, r := range batch {
			key := r.Href
			if key == "" {
				// 没 href 兜底用 title，不至于无脑去重全删
				key = r.Title
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, r)
		}
	}
	return out
}

// isBizOK 检查业务 code —— Bocha 可能是 200 / "200" / 数字或字符串。
func isBizOK(code any) bool {
	switch v := code.(type) {
	case float64:
		return v == 200
	case int:
		return v == 200
	case string:
		return v == "200"
	}
	return false
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
