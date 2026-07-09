package websearch

// BochaProvider —— 国内 web 搜索，直连 api.bochaai.com，中文源覆盖好。
//
// 免费试用：1000 次，注册 https://open.bochaai.com 拿 key，填 BOCHA_API_KEY 环境变量。
type BochaProvider struct {
	apiKey string
}

func NewBochaProvider(apiKey string) *BochaProvider {
	return &BochaProvider{apiKey: apiKey}
}

func (p *BochaProvider) Name() string     { return "bocha" }
func (p *BochaProvider) Endpoint() string { return "https://api.bochaai.com/v1/web-search" }

// 时间窗：我们的统一约定 d/w/m/y → Bocha 的 freshness 取值。
var bochaTimeMap = map[string]string{
	"d": "oneDay",
	"w": "oneWeek",
	"m": "oneMonth",
	"y": "oneYear",
}

func (p *BochaProvider) BuildRequest(
	query string,
	maxResults int,
	timelimit string,
	allowedDomains []string,
	blockedDomains []string,
	topic string,
) (map[string]string, map[string]any) {
	body := map[string]any{
		"query":   query,
		"count":   maxResults,
		"summary": true, // 拿长摘要做 body，不开只有 snippet
	}
	if v, ok := bochaTimeMap[timelimit]; timelimit != "" && ok {
		body["freshness"] = v
	}
	// Bocha 没有原生 topic / 域名白黑名单字段，这三个参数静默忽略
	// （跟 Tavily 一致的口径：不支持的参数不报错，上层 LLM 不必关心 provider 差异）。
	_ = topic
	_ = allowedDomains
	_ = blockedDomains

	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
		"Content-Type":  "application/json",
	}
	return headers, body
}

func (p *BochaProvider) ParseResponse(data map[string]any) []TextSearchResult {
	// 响应结构：data.webPages.value = [ {name, url, snippet, summary}, ... ]
	// 业务 code 非 200（如 quota 用完）由 service 层早抛，parse 只处理 happy path。
	dataObj, _ := data["data"].(map[string]any)
	if dataObj == nil {
		return nil
	}
	webPages, _ := dataObj["webPages"].(map[string]any)
	if webPages == nil {
		return nil
	}
	pages, _ := webPages["value"].([]any)

	out := make([]TextSearchResult, 0, len(pages))
	for _, raw := range pages {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// summary=true 时优先用长摘要，没拿到回落 snippet（短摘要）。
		body := strFrom(p, "summary")
		if body == "" {
			body = strFrom(p, "snippet")
		}
		out = append(out, TextSearchResult{
			Title: strFrom(p, "name"),
			Href:  strFrom(p, "url"),
			Body:  body,
		})
	}
	return out
}
