package websearch

// TavilyProvider —— 海外 web 搜索，直连 api.tavily.com。
//
// 免费额度：1000 次/月，注册 https://app.tavily.com 拿 key，填 TAVILY_API_KEY 环境变量。
type TavilyProvider struct {
	apiKey string
}

func NewTavilyProvider(apiKey string) *TavilyProvider {
	return &TavilyProvider{apiKey: apiKey}
}

func (p *TavilyProvider) Name() string     { return "tavily" }
func (p *TavilyProvider) Endpoint() string { return "https://api.tavily.com/search" }

// 时间窗约定：d/w/m/y = 最近一天 / 一周 / 一月 / 一年 → Tavily 的 time_range。
var tavilyTimeMap = map[string]string{
	"d": "day",
	"w": "week",
	"m": "month",
	"y": "year",
}

// Tavily 支持的 topic 集；不在集内静默忽略（跟 pentaloom 一致的口径：
// 上层 LLM 不必关心 provider 差异）。
var tavilySupportedTopics = map[string]struct{}{
	"finance": {},
	"news":    {},
}

func (p *TavilyProvider) BuildRequest(
	query string,
	maxResults int,
	timelimit string,
	allowedDomains []string,
	blockedDomains []string,
	topic string,
) (map[string]string, map[string]any) {
	body := map[string]any{
		"query":          query,
		"max_results":    maxResults,
		"search_depth":   "basic",   // advanced 双倍 credits，basic 对一般问题够了
		"include_answer": "basic",   // Tavily 自带 LLM 总结一句，可作兜底
	}
	if _, ok := tavilySupportedTopics[topic]; topic != "" && ok {
		body["topic"] = topic
	}
	if v, ok := tavilyTimeMap[timelimit]; timelimit != "" && ok {
		body["time_range"] = v
	}
	if len(allowedDomains) > 0 {
		body["include_domains"] = allowedDomains
	}
	if len(blockedDomains) > 0 {
		body["exclude_domains"] = blockedDomains
	}

	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
		"Content-Type":  "application/json",
	}
	return headers, body
}

func (p *TavilyProvider) ParseResponse(data map[string]any) []TextSearchResult {
	rawResults, _ := data["results"].([]any)
	out := make([]TextSearchResult, 0, len(rawResults))
	for _, r := range rawResults {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, TextSearchResult{
			Title: strFrom(m, "title"),
			Href:  strFrom(m, "url"),
			Body:  strFrom(m, "content"),
		})
	}
	return out
}

func strFrom(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
